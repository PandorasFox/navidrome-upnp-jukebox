package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hecate/navidrome-jukebox/internal/library"
	"github.com/hecate/navidrome-jukebox/internal/models"
	"github.com/hecate/navidrome-jukebox/internal/navidrome"
	"github.com/hecate/navidrome-jukebox/internal/queue"
	"github.com/hecate/navidrome-jukebox/internal/upnp"
)

// UPnP connection states
const (
	UPnPStatusUnconnected = "unconnected"
	UPnPStatusConnecting  = "connecting"
	UPnPStatusConnected   = "connected"
)

// Server holds all application state
type Server struct {
	queueEngine *queue.Engine
	lib         *library.Library
	navidrome   *navidrome.Client
	upnpClient  *upnp.Client
	upnpControl *upnp.ControlPoint
	upnpStatus  string
	upnpMu      sync.RWMutex

	// Playback tracking
	playbackMu   sync.Mutex
	lastTrackURI string // last URI seen from the renderer

	// Config
	navidromeBaseURL string
	rendererName     string
}

// NewServer creates a new server instance
func NewServer(navidromeURL, navidromeUser, navidromePass, rendererAddr string) (*Server, error) {
	const dbPath = "/data/app.db"

	// Create queue engine
	qe, err := queue.NewEngine(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create queue engine: %w", err)
	}

	// Create Navidrome client
	nvClient := navidrome.NewClient(navidromeURL, navidromeUser, navidromePass)

	// Create library manager
	lib, err := library.NewLibrary(dbPath, nvClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create library: %w", err)
	}

	// Create UPnP client
	upnpClient := upnp.NewClient(rendererAddr)

	return &Server{
		queueEngine:      qe,
		lib:              lib,
		navidrome:        nvClient,
		upnpClient:       upnpClient,
		upnpStatus:       UPnPStatusUnconnected,
		navidromeBaseURL: navidromeURL,
		rendererName:     rendererAddr,
	}, nil
}

// StartUPnP discovers and connects to the renderer
func (s *Server) StartUPnP() error {
	s.upnpMu.Lock()
	s.upnpStatus = UPnPStatusConnecting
	s.upnpMu.Unlock()

	log.Printf("Discovering UPnP renderer matching %q...", s.rendererName)
	if err := s.upnpClient.Discover(); err != nil {
		s.upnpMu.Lock()
		s.upnpStatus = UPnPStatusUnconnected
		s.upnpMu.Unlock()
		log.Printf("Failed to discover UPnP renderer: %v", err)
		return fmt.Errorf("failed to discover renderer: %w", err)
	}
	s.upnpControl = s.upnpClient.NewControlPoint()
	s.upnpMu.Lock()
	s.upnpStatus = UPnPStatusConnected
	s.upnpMu.Unlock()
	log.Printf("Connected to UPnP renderer")

	// Pick up current renderer state (in case it's already playing)
	s.pickUpRendererState()

	return nil
}

// pickUpRendererState checks if the renderer is currently playing and syncs our state
func (s *Server) pickUpRendererState() {
	control := s.upnpControl
	if control == nil {
		return
	}

	transportState, err := control.GetTransportInfo("0")
	if err != nil {
		return
	}

	posInfo, err := control.GetPositionInfo("0")
	if err != nil {
		posInfo = &models.PlaybackState{}
	}
	posInfo.TransportState = transportState

	// Derive nowPlaying from the renderer's current URI
	if posInfo.CurrentURI != "" {
		trackID := extractTrackID(posInfo.CurrentURI)
		if trackID != "" {
			trackInfo := s.lib.GetTrackByID(trackID)
			if trackInfo != nil {
				log.Printf("[startup] renderer playing: %q by %s", trackInfo.Title, trackInfo.Artist)
				s.queueEngine.SetNowPlaying(trackInfo)
				s.queueEngine.RemoveByTrackID(trackID)
				if posInfo.Duration == 0 {
					posInfo.Duration = trackInfo.Duration
				}
			}
		}

		s.playbackMu.Lock()
		s.lastTrackURI = posInfo.CurrentURI
		s.playbackMu.Unlock()
	}

	s.queueEngine.SetRenderer(*posInfo)

	if transportState == "PLAYING" || transportState == "PAUSED_PLAYBACK" {
		log.Printf("[startup] renderer is %s, syncing state", transportState)
		s.queueEngine.SetRunning(true)
		// Pre-queue next for gapless
		s.preQueueNext()
	}
}

// GetUPnPStatus returns the current UPnP connection status
func (s *Server) GetUPnPStatus() string {
	s.upnpMu.RLock()
	defer s.upnpMu.RUnlock()
	return s.upnpStatus
}

// ReconnectUPnP retries connecting to the renderer
func (s *Server) ReconnectUPnP() error {
	s.upnpControl = nil
	return s.StartUPnP()
}

// extractTrackID pulls the Navidrome track ID from a stream URL
func extractTrackID(uri string) string {
	// Stream URLs look like: http://host/rest/stream?...&id=TRACKID&...
	if idx := strings.Index(uri, "id="); idx >= 0 {
		id := uri[idx+3:]
		if end := strings.IndexByte(id, '&'); end >= 0 {
			id = id[:end]
		}
		return id
	}
	return ""
}

// StartPlaybackLoop polls the renderer, syncs nowPlaying from its URI,
// removes played tracks from queue, and auto-plays on STOPPED.
func (s *Server) StartPlaybackLoop() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		lastTransportState := ""

		for range ticker.C {
			s.upnpMu.RLock()
			control := s.upnpControl
			s.upnpMu.RUnlock()
			if control == nil {
				continue
			}

			transportState, transErr := control.GetTransportInfo("0")
			if transErr != nil {
				continue
			}

			posInfo, posErr := control.GetPositionInfo("0")
			if posErr != nil {
				posInfo = &models.PlaybackState{}
			}
			posInfo.TransportState = transportState

			// --- Sync nowPlaying from renderer's current URI ---
			if posInfo.CurrentURI != "" {
				s.playbackMu.Lock()
				uriChanged := posInfo.CurrentURI != s.lastTrackURI
				s.lastTrackURI = posInfo.CurrentURI
				s.playbackMu.Unlock()

				if uriChanged {
					trackID := extractTrackID(posInfo.CurrentURI)
					if trackID != "" {
						trackInfo := s.lib.GetTrackByID(trackID)
						if trackInfo != nil {
							log.Printf("[playback] now playing: %q by %s", trackInfo.Title, trackInfo.Artist)
							s.queueEngine.SetNowPlaying(trackInfo)
							s.queueEngine.RemoveByTrackID(trackID)
						}
					}
					// Pre-queue next track for gapless
					s.preQueueNext()
				}
			}

			// Fill duration from nowPlaying if renderer doesn't report it
			if posInfo.Duration == 0 {
				if np := s.queueEngine.NowPlaying(); np != nil {
					posInfo.Duration = np.Duration
				}
			}

			s.queueEngine.SetRenderer(*posInfo)

			// --- Auto-play on STOPPED ---
			if lastTransportState != "STOPPED" && transportState == "STOPPED" {
				s.playNextInQueue()
			}
			lastTransportState = transportState
		}
	}()
}

// playNextInQueue pops queue[0] and plays it, or stops if empty
func (s *Server) playNextInQueue() {
	next := s.queueEngine.PopNext()
	if next != nil {
		s.queueEngine.SetRunning(true)
		if err := s.playTrack(next); err != nil {
			log.Printf("[playback] error playing next: %v", err)
		}
	} else {
		s.queueEngine.SetRunning(false)
	}
}

// preQueueNext sets the next track on the renderer for gapless playback
func (s *Server) preQueueNext() {
	next := s.queueEngine.Peek()
	if next != nil {
		if err := s.queueNextTrack(next); err != nil {
			log.Printf("[playback] failed to pre-queue next: %v", err)
		}
	}
}

// SyncLibrary triggers a library sync
func (s *Server) SyncLibrary(ctx context.Context) error {
	return s.lib.Sync(ctx)
}

// GetLibrary returns the library instance
func (s *Server) GetLibrary() *library.Library {
	return s.lib
}

// isTrackLoaded checks if the renderer currently has a track loaded
// by querying transport info for a non-STOPPED/NO_MEDIA state.
func (s *Server) isTrackLoaded(track *models.QueueItem) bool {
	s.upnpMu.RLock()
	control := s.upnpControl
	s.upnpMu.RUnlock()
	if control == nil {
		return false
	}
	state, err := control.GetTransportInfo("0")
	if err != nil {
		return false
	}
	return state == "PLAYING" || state == "PAUSED_PLAYBACK" || state == "TRANSITIONING"
}

// playTrack plays a track on the UPnP renderer
func (s *Server) playTrack(track *models.QueueItem) error {
	s.upnpMu.RLock()
	control := s.upnpControl
	s.upnpMu.RUnlock()
	if control == nil {
		return fmt.Errorf("upnp not connected")
	}

	streamURL := s.navidrome.StreamURL(track.ID)
	log.Printf("[playTrack] setting URI for %q by %s", track.Title, track.Artist)

	// Build DIDL-Lite metadata
	meta := upnp.DIDLItem(*track, streamURL)

	// Replace cover art placeholder with actual URL
	if track.CoverArt != "" {
		coverURL := s.navidrome.CoverArtURL(track.CoverArt, 300)
		meta = strings.Replace(meta, fmt.Sprintf("__COVER_ART_%s__", track.CoverArt), coverURL, -1)
	}

	// Set and play
	if err := control.SetAVTransportURI("0", streamURL, meta); err != nil {
		return fmt.Errorf("failed to set URI: %w", err)
	}
	log.Printf("[playTrack] URI set, sending Play")

	if err := control.Play("1"); err != nil {
		return fmt.Errorf("failed to play: %w", err)
	}

	log.Printf("[playTrack] playing")
	return nil
}

// queueNextTrack sets the next track on the renderer for gapless playback
func (s *Server) queueNextTrack(track *models.QueueItem) error {
	s.upnpMu.RLock()
	control := s.upnpControl
	s.upnpMu.RUnlock()
	if control == nil {
		return fmt.Errorf("upnp not connected")
	}

	streamURL := s.navidrome.StreamURL(track.ID)
	meta := upnp.DIDLItem(*track, streamURL)

	if track.CoverArt != "" {
		coverURL := s.navidrome.CoverArtURL(track.CoverArt, 300)
		meta = strings.Replace(meta, fmt.Sprintf("__COVER_ART_%s__", track.CoverArt), coverURL, -1)
	}

	log.Printf("[queueNext] queuing next: %q by %s", track.Title, track.Artist)
	return control.SetNextAVTransportURI("0", streamURL, meta)
}

// Routes sets up HTTP routes
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// CORS middleware
	r.Use(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			h.ServeHTTP(w, r)
		})
	})

	// Frontend - serve React SPA
	r.Get("/", s.handleFrontend)
	r.Get("/index.html", s.handleFrontend)
	r.Get("/assets/*", s.handleStatic)
	r.Get("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "frontend/favicon.svg")
	})
	r.Get("/{*path}", s.handleFrontend) // Catch-all for SPA routes

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/search", s.handleSearch)
		r.Get("/artist/albums", s.handleArtistAlbums)
		r.Get("/album/tracks", s.handleAlbumTracks)
		r.Get("/queue", s.handleGetQueue)
		r.Post("/queue/add", s.handleAddToQueue)
		r.Post("/queue/add-album", s.handleAddAlbum)
		r.Delete("/queue/{idx}", s.handleRemoveFromQueue)
		r.Post("/queue/clear", s.handleClearQueue)
		r.Post("/queue/shuffle", s.handleShuffleQueue)
		r.Get("/state", s.handleGetState)
		r.Post("/play", s.handlePlay)
		r.Post("/pause", s.handlePause)
		r.Post("/stop", s.handleStop)
		r.Post("/next", s.handleNext)
		r.Post("/seek/{idx}", s.handleSeek)
		r.Get("/sse", s.handleSSE)
		r.Get("/cover/{id}", s.handleCoverArt)
		r.Get("/sync/status", s.handleSyncStatus)
		r.Post("/sync", s.handleSync)
		r.Get("/upnp/status", s.handleUPnPStatus)
		r.Post("/upnp/reconnect", s.handleUPnPReconnect)
	})

	return r
}

// handleFrontend serves the React frontend
func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	// Serve the static HTML file
	http.ServeFile(w, r, "frontend/index.html")
}

// handleStatic serves static files (JS/CSS assets)
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	http.ServeFile(w, r, fmt.Sprintf("frontend/assets/%s", path))
}

// handleSearch handles track and album search
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	searchType := r.URL.Query().Get("type") // "tracks" (default) or "albums"
	log.Printf("[search] query=%q type=%q", query, searchType)

	w.Header().Set("Content-Type", "application/json")

	if query == "" {
		switch searchType {
		case "albums":
			w.Write([]byte("{\"albums\":[]}"))
		case "artists":
			w.Write([]byte("{\"artists\":[]}"))
		default:
			w.Write([]byte("{\"tracks\":[]}"))
		}
		return
	}

	switch searchType {
	case "albums":
		results, err := s.lib.SearchAlbums(query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string][]map[string]interface{}{"albums": results})
	case "artists":
		results, err := s.lib.SearchArtists(query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string][]map[string]interface{}{"artists": results})
	default:
		results, err := s.lib.Search(query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string][]map[string]interface{}{"tracks": results})
	}
}

// handleArtistAlbums returns albums by a given artist
func (s *Server) handleArtistAlbums(w http.ResponseWriter, r *http.Request) {
	artist := r.URL.Query().Get("artist")
	if artist == "" {
		http.Error(w, "artist required", http.StatusBadRequest)
		return
	}
	results, err := s.lib.GetArtistAlbums(artist)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]map[string]interface{}{"albums": results})
}

// handleAlbumTracks returns tracks for a given album
func (s *Server) handleAlbumTracks(w http.ResponseWriter, r *http.Request) {
	albumID := r.URL.Query().Get("albumId")
	if albumID == "" {
		http.Error(w, "albumId required", http.StatusBadRequest)
		return
	}
	results, err := s.lib.GetAlbumTracks(albumID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]map[string]interface{}{"tracks": results})
}

// handleGetQueue returns the current queue
func (s *Server) handleGetQueue(w http.ResponseWriter, r *http.Request) {
	state := s.queueEngine.State()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// handleAddToQueue adds a track to the queue
func (s *Server) handleAddToQueue(w http.ResponseWriter, r *http.Request) {
	var item models.QueueItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.URL.Query().Get("next") == "1" {
		s.queueEngine.InsertNext(item)
	} else {
		s.queueEngine.Add(item)
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleAddAlbum adds all tracks from an album to the queue in order
func (s *Server) handleAddAlbum(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlbumID string `json:"albumId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.AlbumID == "" {
		http.Error(w, "albumId required", http.StatusBadRequest)
		return
	}

	tracks, err := s.lib.GetAlbumTracks(req.AlbumID)
	if err != nil {
		log.Printf("[addAlbum] error fetching tracks: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	insertNext := r.URL.Query().Get("next") == "1"
	if insertNext {
		// Insert in reverse so they stack in correct album order after current track
		for i := len(tracks) - 1; i >= 0; i-- {
			t := tracks[i]
			item := models.QueueItem{
				ID:       t["id"].(string),
				Title:    t["title"].(string),
				Artist:   t["artist"].(string),
				Album:    t["album"].(string),
				Duration: t["duration"].(int),
				CoverArt: t["coverArt"].(string),
			}
			s.queueEngine.InsertNext(item)
		}
	} else {
		for _, t := range tracks {
			item := models.QueueItem{
				ID:       t["id"].(string),
				Title:    t["title"].(string),
				Artist:   t["artist"].(string),
				Album:    t["album"].(string),
				Duration: t["duration"].(int),
				CoverArt: t["coverArt"].(string),
			}
			s.queueEngine.Add(item)
		}
	}

	log.Printf("[addAlbum] queued %d tracks for album %s", len(tracks), req.AlbumID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "tracksAdded": len(tracks)})
}

// handleRemoveFromQueue removes a track from the queue
func (s *Server) handleRemoveFromQueue(w http.ResponseWriter, r *http.Request) {
	idxStr := chi.URLParam(r, "idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	s.queueEngine.Remove(idx)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleClearQueue clears the queue
func (s *Server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	s.queueEngine.Clear()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleShuffleQueue shuffles the queue
func (s *Server) handleShuffleQueue(w http.ResponseWriter, r *http.Request) {
	s.queueEngine.Shuffle()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleGetState returns current state
func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	state := s.queueEngine.State()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// handlePlay starts playback
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	np := s.queueEngine.NowPlaying()

	if np != nil && s.isTrackLoaded(np) {
		// Track loaded, just resume
		log.Printf("[play] resuming playback")
		s.upnpMu.RLock()
		control := s.upnpControl
		s.upnpMu.RUnlock()
		if control != nil {
			if err := control.Play("1"); err != nil {
				log.Printf("[play] resume error: %v", err)
			}
		}
		s.queueEngine.SetRunning(true)
	} else {
		// Nothing loaded — pop next from queue
		next := s.queueEngine.PopNext()
		if next == nil {
			log.Printf("[play] queue is empty, nothing to play")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{\"ok\":true}"))
			return
		}
		s.queueEngine.SetRunning(true)
		log.Printf("[play] starting: %s - %s", next.Artist, next.Title)
		if err := s.playTrack(next); err != nil {
			log.Printf("[play] error: %v", err)
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handlePause pauses playback
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.upnpMu.RLock()
	control := s.upnpControl
	s.upnpMu.RUnlock()
	if control != nil {
		control.Pause()
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleStop stops playback
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.upnpMu.RLock()
	control := s.upnpControl
	s.upnpMu.RUnlock()
	if control != nil {
		control.Stop()
	}
	s.queueEngine.SetRunning(false)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleNext skips to next track
func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	s.playNextInQueue()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleSeek plays a specific track from the queue by index (removes it from queue)
func (s *Server) handleSeek(w http.ResponseWriter, r *http.Request) {
	idxStr := chi.URLParam(r, "idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	// Get the track before removing
	state := s.queueEngine.State()
	if idx < 0 || idx >= len(state.Queue) {
		http.Error(w, "Index out of range", http.StatusBadRequest)
		return
	}
	track := state.Queue[idx]
	s.queueEngine.Remove(idx)

	s.queueEngine.SetRunning(true)
	if err := s.playTrack(&track); err != nil {
		log.Printf("Error playing track: %v", err)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"ok\":true}"))
}

// handleSSE serves Server-Sent Events for real-time updates
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, _ := w.(http.Flusher)

	ch := s.queueEngine.Subscribe()
	defer s.queueEngine.Unsubscribe(ch)

	// Send initial state
	state := s.queueEngine.State()
	data, _ := json.Marshal(state)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleCoverArt proxies cover art requests
func (s *Server) handleCoverArt(w http.ResponseWriter, r *http.Request) {
	coverArtID := chi.URLParam(r, "id")
	if coverArtID == "" {
		http.Error(w, "No cover art ID", http.StatusBadRequest)
		return
	}

	// Fetch from Navidrome
	url := s.navidrome.CoverArtURL(coverArtID, 300)
	resp, err := http.Get(url)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	io.Copy(w, resp.Body)
}

// handleSyncStatus returns the current sync status
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	lib := s.GetLibrary()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"isSyncing": lib.IsSyncing(),
		"songCount": lib.GetSongCount(),
	})
}

// handleSync triggers a library sync
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.lib.IsSyncing() {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "sync already in progress"})
		return
	}

	go func() {
		ctx := context.Background()
		if err := s.SyncLibrary(ctx); err != nil {
			log.Printf("Library sync failed: %v", err)
		}
	}()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "sync started"})
}

// handleUPnPStatus returns the current UPnP connection status
func (s *Server) handleUPnPStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": s.GetUPnPStatus(),
	})
}

// handleUPnPReconnect retries connecting to the renderer
func (s *Server) handleUPnPReconnect(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := s.ReconnectUPnP(); err != nil {
			log.Printf("UPnP reconnect failed: %v", err)
		} else {
			log.Printf("UPnP reconnect succeeded")
		}
	}()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "reconnect initiated"})
}

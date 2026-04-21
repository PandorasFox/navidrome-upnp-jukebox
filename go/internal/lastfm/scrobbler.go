package lastfm

import (
	"log"
	"sync"
	"time"
)

// ScrobbleTrack holds the metadata needed for scrobbling.
type ScrobbleTrack struct {
	Artist   string
	Title    string
	Album    string
	Duration int // seconds
}

// Scrobbler coordinates scrobble timing and dispatches API calls to active listeners.
type Scrobbler struct {
	client *Client
	store  *Store

	mu            sync.Mutex
	currentTrack  *ScrobbleTrack
	playStartTime time.Time // wall-clock time when track started (used as scrobble timestamp)
	playedSeconds int       // accumulated renderer play time
	lastPosition  int       // last renderer position seen
	scrobbled     bool      // already scrobbled current track
}

func NewScrobbler(client *Client, store *Store) *Scrobbler {
	return &Scrobbler{
		client: client,
		store:  store,
	}
}

// Store exposes the store for use by HTTP handlers.
func (s *Scrobbler) Store() *Store { return s.store }

// Enabled returns true if the scrobbler has a configured client.
func (s *Scrobbler) Enabled() bool { return s.client != nil }

// AuthURL returns the Last.fm auth redirect URL.
func (s *Scrobbler) AuthURL(callbackURL string) string {
	return s.client.AuthURL(callbackURL)
}

// GetSession exchanges a callback token for a permanent session key.
func (s *Scrobbler) GetSession(token string) (sessionKey, username string, err error) {
	return s.client.GetSession(token)
}

// OnTrackChange is called when the playback loop detects a new track URI.
// It scrobbles the previous track if eligible, resets state, and fires nowPlaying.
func (s *Scrobbler) OnTrackChange(track *ScrobbleTrack) {
	s.mu.Lock()
	// Scrobble the previous track if eligible
	if s.currentTrack != nil && !s.scrobbled && s.isEligible() {
		prev := s.currentTrack
		ts := s.playStartTime.Unix()
		go s.scrobbleToListeners(prev, ts)
	}

	// Reset for new track
	s.currentTrack = track
	s.playStartTime = time.Now()
	s.playedSeconds = 0
	s.lastPosition = 0
	s.scrobbled = false
	s.mu.Unlock()

	if track != nil {
		go s.nowPlayingToListeners(track)
	}
}

// OnPositionUpdate is called every tick with the renderer's current position.
// It accumulates play time and triggers scrobble when the threshold is reached.
func (s *Scrobbler) OnPositionUpdate(position, duration int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentTrack == nil || s.scrobbled {
		return
	}

	// Accumulate play time from position delta.
	// Only count forward progress within a reasonable window (handles polling jitter).
	// Skips backwards seeks, large jumps, and stalls.
	delta := position - s.lastPosition
	if delta > 0 && delta <= 5 {
		s.playedSeconds += delta
	}
	s.lastPosition = position

	// Use track's known duration if renderer doesn't report one (common for streams)
	dur := duration
	if dur == 0 {
		dur = s.currentTrack.Duration
	}

	if dur > 0 && s.isEligibleWith(dur) {
		s.scrobbled = true
		track := s.currentTrack
		ts := s.playStartTime.Unix()
		go s.scrobbleToListeners(track, ts)
	}
}

// OnStop is called when the stop button is pressed.
// Scrobbles current track if eligible, then clears all listening sessions.
func (s *Scrobbler) OnStop() {
	s.mu.Lock()
	if s.currentTrack != nil && !s.scrobbled && s.isEligible() {
		track := s.currentTrack
		ts := s.playStartTime.Unix()
		go s.scrobbleToListeners(track, ts)
	}
	s.currentTrack = nil
	s.scrobbled = false
	s.playedSeconds = 0
	s.mu.Unlock()

	if err := s.store.ClearAllListening(); err != nil {
		log.Printf("[lastfm] failed to clear listening sessions: %v", err)
	}
}

// isEligible checks if playedSeconds meets the scrobble threshold using the track's own duration.
func (s *Scrobbler) isEligible() bool {
	return s.isEligibleWith(s.currentTrack.Duration)
}

// isEligibleWith checks the Last.fm scrobble rule: played ≥50% of duration OR ≥4 minutes.
func (s *Scrobbler) isEligibleWith(duration int) bool {
	if s.playedSeconds >= 240 {
		return true
	}
	if duration > 0 && s.playedSeconds >= duration/2 {
		return true
	}
	return false
}

func (s *Scrobbler) scrobbleToListeners(track *ScrobbleTrack, timestamp int64) {
	listeners, err := s.store.GetActiveListeners()
	if err != nil {
		log.Printf("[lastfm] failed to get listeners for scrobble: %v", err)
		return
	}
	for _, li := range listeners {
		go func(sk, user string) {
			if err := s.client.Scrobble(sk, track.Artist, track.Title, track.Album, timestamp, track.Duration); err != nil {
				log.Printf("[lastfm] scrobble failed for %s: %v", user, err)
			} else {
				log.Printf("[lastfm] scrobbled %q by %s for %s", track.Title, track.Artist, user)
			}
		}(li.SessionKey, li.LastFMUser)
	}
}

func (s *Scrobbler) nowPlayingToListeners(track *ScrobbleTrack) {
	listeners, err := s.store.GetActiveListeners()
	if err != nil {
		log.Printf("[lastfm] failed to get listeners for nowPlaying: %v", err)
		return
	}
	for _, li := range listeners {
		go func(sk, user string) {
			if err := s.client.UpdateNowPlaying(sk, track.Artist, track.Title, track.Album, track.Duration); err != nil {
				log.Printf("[lastfm] nowPlaying failed for %s: %v", user, err)
			}
		}(li.SessionKey, li.LastFMUser)
	}
}

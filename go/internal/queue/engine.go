package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/hecate/navidrome-jukebox/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

// Engine manages the playback queue and state.
// The queue holds upcoming tracks only; nowPlaying is set by the server
// based on what the renderer is actually playing.
type Engine struct {
	mu         sync.RWMutex
	queue      []models.QueueItem
	nowPlaying *models.QueueItem
	isRunning  bool
	renderer   models.PlaybackState
	db         *sql.DB

	// SSE subscribers
	subMu       sync.RWMutex
	subscribers map[chan []byte]struct{}
}

// NewEngine creates a new queue engine
func NewEngine(dbPath string) (*Engine, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			track_id TEXT NOT NULL,
			title TEXT NOT NULL,
			artist TEXT NOT NULL,
			album TEXT NOT NULL,
			year INTEGER,
			duration INTEGER NOT NULL,
			cover_art TEXT,
			added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			position INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_queue_position ON queue(position);
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	e := &Engine{
		queue:       make([]models.QueueItem, 0),
		isRunning:   false,
		db:          db,
		subscribers: make(map[chan []byte]struct{}),
	}

	if err := e.loadQueue(); err != nil {
		return nil, fmt.Errorf("failed to load queue: %w", err)
	}

	return e, nil
}

// loadQueue loads the queue from database
func (e *Engine) loadQueue() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rows, err := e.db.Query("SELECT track_id, title, artist, album, year, duration, cover_art, position FROM queue ORDER BY position")
	if err != nil {
		return err
	}
	defer rows.Close()

	e.queue = make([]models.QueueItem, 0)
	for rows.Next() {
		var item models.QueueItem
		var year sql.NullInt64
		var position int
		err := rows.Scan(&item.ID, &item.Title, &item.Artist, &item.Album, &year, &item.Duration, &item.CoverArt, &position)
		if err != nil {
			return err
		}
		if year.Valid {
			item.Year = int(year.Int64)
		}
		item.AddedAt = time.Time{}
		item.AddedBy = "system"
		e.queue = append(e.queue, item)
	}

	return rows.Err()
}

// saveQueue persists the queue to database
func (e *Engine) saveQueue() error {
	_, err := e.db.Exec("DELETE FROM queue")
	if err != nil {
		return err
	}

	stmt, err := e.db.Prepare("INSERT INTO queue (track_id, title, artist, album, year, duration, cover_art, position) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, item := range e.queue {
		year := sql.NullInt64{Int64: int64(item.Year), Valid: item.Year > 0}
		_, err := stmt.Exec(item.ID, item.Title, item.Artist, item.Album, year, item.Duration, item.CoverArt, i)
		if err != nil {
			return err
		}
	}

	return nil
}

// Add adds a track to the end of the queue
func (e *Engine) Add(item models.QueueItem) {
	e.mu.Lock()
	item.AddedAt = time.Now()
	item.AddedBy = "system"
	e.queue = append(e.queue, item)
	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// InsertNext adds a track at position 0 (next up)
func (e *Engine) InsertNext(item models.QueueItem) {
	e.mu.Lock()
	item.AddedAt = time.Now()
	item.AddedBy = "system"
	e.queue = append([]models.QueueItem{item}, e.queue...)
	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// PopNext removes and returns queue[0], or nil if empty
func (e *Engine) PopNext() *models.QueueItem {
	e.mu.Lock()
	if len(e.queue) == 0 {
		e.mu.Unlock()
		return nil
	}

	item := e.queue[0]
	e.queue = e.queue[1:]
	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
	return &item
}

// Peek returns queue[0] without removing it, or nil if empty
func (e *Engine) Peek() *models.QueueItem {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.queue) == 0 {
		return nil
	}
	item := e.queue[0]
	return &item
}

// RemoveByTrackID removes the first occurrence of a track ID from the queue
func (e *Engine) RemoveByTrackID(trackID string) bool {
	e.mu.Lock()
	for i, item := range e.queue {
		if item.ID == trackID {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)
			if err := e.saveQueue(); err != nil {
				fmt.Printf("Failed to save queue: %v\n", err)
			}
			e.broadcastLocked()
			e.mu.Unlock()
			return true
		}
	}
	e.mu.Unlock()
	return false
}

// Remove removes a track from the queue by index
func (e *Engine) Remove(idx int) bool {
	e.mu.Lock()
	if idx < 0 || idx >= len(e.queue) {
		e.mu.Unlock()
		return false
	}

	e.queue = append(e.queue[:idx], e.queue[idx+1:]...)
	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
	return true
}

// Clear removes all tracks from the queue
func (e *Engine) Clear() {
	e.mu.Lock()
	e.queue = make([]models.QueueItem, 0)
	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// Shuffle randomizes the queue
func (e *Engine) Shuffle() {
	e.mu.Lock()
	if len(e.queue) <= 1 {
		e.mu.Unlock()
		return
	}

	rand.Shuffle(len(e.queue), func(i, j int) {
		e.queue[i], e.queue[j] = e.queue[j], e.queue[i]
	})

	if err := e.saveQueue(); err != nil {
		fmt.Printf("Failed to save queue: %v\n", err)
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// NowPlaying returns the currently playing track
func (e *Engine) NowPlaying() *models.QueueItem {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.nowPlaying
}

// SetNowPlaying sets the currently playing track (derived from renderer state)
func (e *Engine) SetNowPlaying(item *models.QueueItem) {
	e.mu.Lock()
	e.nowPlaying = item
	e.broadcastLocked()
	e.mu.Unlock()
}

// IsRunning returns whether playback is active
func (e *Engine) IsRunning() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isRunning
}

// SetRunning sets the running state
func (e *Engine) SetRunning(running bool) {
	e.mu.Lock()
	e.isRunning = running
	if !running {
		e.nowPlaying = nil
	}
	e.broadcastLocked()
	e.mu.Unlock()
}

// SetRenderer updates the renderer playback state and broadcasts
func (e *Engine) SetRenderer(state models.PlaybackState) {
	e.mu.Lock()
	e.renderer = state
	e.broadcastLocked()
	e.mu.Unlock()
}

// QueueLen returns the number of items in the queue
func (e *Engine) QueueLen() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.queue)
}

// State returns the current system state
func (e *Engine) State() models.SystemState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	queue := make([]models.QueueItem, len(e.queue))
	copy(queue, e.queue)

	return models.SystemState{
		Queue:      queue,
		NowPlaying: e.nowPlaying,
		IsRunning:  e.isRunning,
		Renderer:   e.renderer,
	}
}

// broadcastLocked sends current state to all subscribers.
// Must be called while e.mu is held.
func (e *Engine) broadcastLocked() {
	state := models.SystemState{
		Queue:      make([]models.QueueItem, len(e.queue)),
		NowPlaying: e.nowPlaying,
		IsRunning:  e.isRunning,
		Renderer:   e.renderer,
	}
	copy(state.Queue, e.queue)
	data, _ := json.Marshal(state)

	e.subMu.RLock()
	for ch := range e.subscribers {
		select {
		case ch <- data:
		default:
		}
	}
	e.subMu.RUnlock()
}

// Subscribe returns a new channel that receives state broadcasts
func (e *Engine) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	e.subMu.Lock()
	e.subscribers[ch] = struct{}{}
	e.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (e *Engine) Unsubscribe(ch chan []byte) {
	e.subMu.Lock()
	delete(e.subscribers, ch)
	e.subMu.Unlock()
}

// Close closes the database connection
func (e *Engine) Close() error {
	return e.db.Close()
}

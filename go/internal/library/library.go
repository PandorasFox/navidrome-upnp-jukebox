package library

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"

	"github.com/hecate/navidrome-jukebox/internal/models"
	"github.com/hecate/navidrome-jukebox/internal/navidrome"
	_ "github.com/mattn/go-sqlite3"
)

// Library manages the local song cache and sync
type Library struct {
	db        *sql.DB
	client    *navidrome.Client
	mu        sync.RWMutex
	isSyncing bool
	songCount int
}

// NewLibrary creates a new library manager
func NewLibrary(dbPath string, client *navidrome.Client) (*Library, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS songs (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			artist TEXT NOT NULL,
			album TEXT NOT NULL,
			album_id TEXT DEFAULT '',
			track_number INTEGER DEFAULT 0,
			duration INTEGER DEFAULT 0,
			cover_art TEXT
		);
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	// Migrate: add columns if missing (existing DBs), then create indexes
	db.Exec(`ALTER TABLE songs ADD COLUMN duration INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE songs ADD COLUMN album_id TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE songs ADD COLUMN track_number INTEGER DEFAULT 0`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_songs_title ON songs(title)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_songs_album ON songs(album)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_songs_album_id ON songs(album_id)`)

	l := &Library{
		db:     db,
		client: client,
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM songs").Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("failed to count songs: %w", err)
	}
	l.songCount = count

	return l, nil
}

// Sync performs a full library sync from Navidrome
func (l *Library) Sync(ctx context.Context) error {
	l.mu.Lock()
	if l.isSyncing {
		l.mu.Unlock()
		return nil
	}
	l.isSyncing = true
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		l.isSyncing = false
		l.mu.Unlock()
	}()

	fmt.Println("Starting library sync...")

	const pageSize = 500
	var allSongs []models.SearchTrack
	var songOffset int

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		songs, err := l.client.SearchAll(pageSize, songOffset)
		if err != nil {
			return fmt.Errorf("failed to search: %w", err)
		}

		allSongs = append(allSongs, songs...)
		fmt.Printf("Fetched %d songs (total: %d)\n", len(songs), len(allSongs))

		if len(songs) < pageSize {
			break
		}
		songOffset += pageSize
	}

	tx, err := l.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec("DELETE FROM songs")
	if err != nil {
		return fmt.Errorf("failed to clear songs: %w", err)
	}

	insertStmt, err := tx.Prepare("INSERT OR REPLACE INTO songs (id, title, artist, album, album_id, track_number, duration, cover_art) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer insertStmt.Close()

	for _, song := range allSongs {
		_, err := insertStmt.Exec(song.ID, song.Title, song.Artist, song.Album, song.AlbumID, song.Track, song.Duration, song.CoverArt)
		if err != nil {
			fmt.Printf("Warning: failed to insert song %s: %v\n", song.Title, err)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	l.mu.Lock()
	l.songCount = len(allSongs)
	l.mu.Unlock()

	fmt.Printf("Library sync complete: %d songs\n", len(allSongs))
	return nil
}

// Search searches for songs by title
func (l *Library) Search(query string) ([]map[string]interface{}, error) {
	rows, err := l.db.Query(`
		SELECT id, title, artist, album, COALESCE(duration, 0), COALESCE(cover_art, '')
		FROM songs
		WHERE title LIKE ?
		ORDER BY
			CASE
				WHEN title LIKE ? THEN 0
				WHEN title LIKE ? THEN 1
				ELSE 2
			END,
			title
		LIMIT 100
	`, "%"+query+"%", query, query+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id, title, artist, album, coverArt string
		var duration int
		if err := rows.Scan(&id, &title, &artist, &album, &duration, &coverArt); err != nil {
			log.Printf("[library.Search] scan error: %v", err)
			continue
		}
		results = append(results, map[string]interface{}{
			"id":       id,
			"title":    title,
			"artist":   artist,
			"album":    album,
			"duration": duration,
			"coverArt": coverArt,
		})
	}

	return results, rows.Err()
}

// SearchAlbums searches for albums by name, returning grouped results
func (l *Library) SearchAlbums(query string) ([]map[string]interface{}, error) {
	rows, err := l.db.Query(`
		SELECT album_id, album, MIN(artist), MIN(COALESCE(cover_art, '')),
		       COUNT(*) as track_count, SUM(COALESCE(duration, 0)) as total_duration
		FROM songs
		WHERE album LIKE ?
		GROUP BY album_id, album
		ORDER BY album
		LIMIT 50
	`, "%"+query+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to search albums: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var albumID, album, artist, coverArt string
		var trackCount, totalDuration int
		if err := rows.Scan(&albumID, &album, &artist, &coverArt, &trackCount, &totalDuration); err != nil {
			log.Printf("[library.SearchAlbums] scan error: %v", err)
			continue
		}
		results = append(results, map[string]interface{}{
			"albumId":       albumID,
			"album":         album,
			"artist":        artist,
			"coverArt":      coverArt,
			"trackCount":    trackCount,
			"totalDuration": totalDuration,
		})
	}

	return results, rows.Err()
}

// SearchArtists searches for distinct artists by name
func (l *Library) SearchArtists(query string) ([]map[string]interface{}, error) {
	rows, err := l.db.Query(`
		SELECT artist, MIN(COALESCE(cover_art, '')), COUNT(DISTINCT album_id) as album_count
		FROM songs
		WHERE artist LIKE ?
		GROUP BY artist
		ORDER BY artist
		LIMIT 50
	`, "%"+query+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to search artists: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var artist, coverArt string
		var albumCount int
		if err := rows.Scan(&artist, &coverArt, &albumCount); err != nil {
			log.Printf("[library.SearchArtists] scan error: %v", err)
			continue
		}
		results = append(results, map[string]interface{}{
			"artist":     artist,
			"coverArt":   coverArt,
			"albumCount": albumCount,
		})
	}

	return results, rows.Err()
}

// GetArtistAlbums returns all albums by a given artist
func (l *Library) GetArtistAlbums(artist string) ([]map[string]interface{}, error) {
	rows, err := l.db.Query(`
		SELECT album_id, album, MIN(COALESCE(cover_art, '')),
		       COUNT(*) as track_count, SUM(COALESCE(duration, 0)) as total_duration
		FROM songs
		WHERE artist = ?
		GROUP BY album_id, album
		ORDER BY album
	`, artist)
	if err != nil {
		return nil, fmt.Errorf("failed to get artist albums: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var albumID, album, coverArt string
		var trackCount, totalDuration int
		if err := rows.Scan(&albumID, &album, &coverArt, &trackCount, &totalDuration); err != nil {
			log.Printf("[library.GetArtistAlbums] scan error: %v", err)
			continue
		}
		results = append(results, map[string]interface{}{
			"albumId":       albumID,
			"album":         album,
			"artist":        artist,
			"coverArt":      coverArt,
			"trackCount":    trackCount,
			"totalDuration": totalDuration,
		})
	}

	return results, rows.Err()
}

// GetAlbumTracks returns all tracks for an album, ordered by track number
func (l *Library) GetAlbumTracks(albumID string) ([]map[string]interface{}, error) {
	rows, err := l.db.Query(`
		SELECT id, title, artist, album, COALESCE(duration, 0), COALESCE(cover_art, ''), track_number
		FROM songs
		WHERE album_id = ?
		ORDER BY track_number, title
	`, albumID)
	if err != nil {
		return nil, fmt.Errorf("failed to get album tracks: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id, title, artist, album, coverArt string
		var duration, trackNumber int
		if err := rows.Scan(&id, &title, &artist, &album, &duration, &coverArt, &trackNumber); err != nil {
			log.Printf("[library.GetAlbumTracks] scan error: %v", err)
			continue
		}
		results = append(results, map[string]interface{}{
			"id":       id,
			"title":    title,
			"artist":   artist,
			"album":    album,
			"duration": duration,
			"coverArt": coverArt,
			"track":    trackNumber,
		})
	}

	return results, rows.Err()
}

// GetTrackByID looks up a single track by its Navidrome ID
func (l *Library) GetTrackByID(trackID string) *models.QueueItem {
	var id, title, artist, album, coverArt string
	var duration int
	err := l.db.QueryRow(`
		SELECT id, title, artist, album, COALESCE(duration, 0), COALESCE(cover_art, '')
		FROM songs WHERE id = ?
	`, trackID).Scan(&id, &title, &artist, &album, &duration, &coverArt)
	if err != nil {
		return nil
	}
	return &models.QueueItem{
		ID:       id,
		Title:    title,
		Artist:   artist,
		Album:    album,
		Duration: duration,
		CoverArt: coverArt,
	}
}

func (l *Library) GetSongCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.songCount
}

func (l *Library) IsSyncing() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isSyncing
}

func (l *Library) Close() error {
	return l.db.Close()
}

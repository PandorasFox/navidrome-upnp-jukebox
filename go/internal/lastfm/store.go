package lastfm

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// User represents a jukebox session user.
type User struct {
	ID           string
	Name         string
	SessionToken string
	LastFMUser   string // empty if not linked
	IsListening  bool
}

// Listener is an active user with a Last.fm session key.
type Listener struct {
	UserID     string
	SessionKey string
	LastFMUser string
	Name       string
}

// Store manages user, Last.fm link, and listening session persistence.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("lastfm store: open db: %w", err)
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			session_token TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS lastfm_links (
			user_id TEXT PRIMARY KEY REFERENCES users(id),
			session_key TEXT NOT NULL,
			lastfm_user TEXT NOT NULL,
			linked_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS listening_sessions (
			user_id TEXT PRIMARY KEY REFERENCES users(id),
			until DATETIME NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("lastfm store: migrate: %w", err)
		}
	}

	return &Store{db: db}, nil
}

// GetOrCreateUser looks up a user by session token, creating one if needed.
func (s *Store) GetOrCreateUser(sessionToken string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, name, session_token FROM users WHERE session_token = ?`,
		sessionToken,
	).Scan(&u.ID, &u.Name, &u.SessionToken)
	if err == nil {
		return &u, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	u = User{
		ID:           newUUID(),
		SessionToken: sessionToken,
	}
	_, err = s.db.Exec(
		`INSERT INTO users (id, name, session_token) VALUES (?, '', ?)`,
		u.ID, u.SessionToken,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// SetUserName updates a user's display name.
func (s *Store) SetUserName(userID, name string) error {
	_, err := s.db.Exec(`UPDATE users SET name = ? WHERE id = ?`, name, userID)
	return err
}

// GetUser returns a user with Last.fm link and listening status populated.
func (s *Store) GetUser(userID string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, name, session_token FROM users WHERE id = ?`, userID,
	).Scan(&u.ID, &u.Name, &u.SessionToken)
	if err != nil {
		return nil, err
	}

	// Check Last.fm link
	var lfmUser string
	err = s.db.QueryRow(
		`SELECT lastfm_user FROM lastfm_links WHERE user_id = ?`, userID,
	).Scan(&lfmUser)
	if err == nil {
		u.LastFMUser = lfmUser
	}

	// Check listening session
	var until time.Time
	err = s.db.QueryRow(
		`SELECT until FROM listening_sessions WHERE user_id = ?`, userID,
	).Scan(&until)
	if err == nil && until.After(time.Now()) {
		u.IsListening = true
	}

	return &u, nil
}

// LinkLastFM stores a Last.fm session key for a user.
func (s *Store) LinkLastFM(userID, sessionKey, lastfmUser string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO lastfm_links (user_id, session_key, lastfm_user) VALUES (?, ?, ?)`,
		userID, sessionKey, lastfmUser,
	)
	return err
}

// UnlinkLastFM removes a user's Last.fm link and listening session.
func (s *Store) UnlinkLastFM(userID string) error {
	_, err := s.db.Exec(`DELETE FROM lastfm_links WHERE user_id = ?`, userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM listening_sessions WHERE user_id = ?`, userID)
	return err
}

// GetLastFMLink returns the session key and username for a user's Last.fm link.
// Returns sql.ErrNoRows if not linked.
func (s *Store) GetLastFMLink(userID string) (sessionKey, lastfmUser string, err error) {
	err = s.db.QueryRow(
		`SELECT session_key, lastfm_user FROM lastfm_links WHERE user_id = ?`, userID,
	).Scan(&sessionKey, &lastfmUser)
	return
}

// SetListening marks a user as listening until the given time.
func (s *Store) SetListening(userID string, until time.Time) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO listening_sessions (user_id, until) VALUES (?, ?)`,
		userID, until,
	)
	return err
}

// ClearAllListening removes all listening sessions (called on stop).
func (s *Store) ClearAllListening() error {
	_, err := s.db.Exec(`DELETE FROM listening_sessions`)
	return err
}

// GetActiveListeners returns all users with a valid Last.fm link and unexpired listening session.
func (s *Store) GetActiveListeners() ([]Listener, error) {
	rows, err := s.db.Query(
		`SELECT u.id, l.session_key, l.lastfm_user, u.name
		 FROM listening_sessions ls
		 JOIN lastfm_links l ON l.user_id = ls.user_id
		 JOIN users u ON u.id = ls.user_id
		 WHERE ls.until > datetime('now')`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var listeners []Listener
	for rows.Next() {
		var li Listener
		if err := rows.Scan(&li.UserID, &li.SessionKey, &li.LastFMUser, &li.Name); err != nil {
			return nil, err
		}
		listeners = append(listeners, li)
	}
	return listeners, rows.Err()
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

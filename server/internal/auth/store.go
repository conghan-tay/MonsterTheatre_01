package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

var (
	// ErrNotFound means no such director or session.
	ErrNotFound = errors.New("not found")
	// ErrUsernameTaken means a director with that username already exists.
	ErrUsernameTaken = errors.New("username already taken")
)

// Director is a director account.
type Director struct {
	ID           string
	Username     string
	PasswordHash string
}

// Session is a logged-in director's session.
type Session struct {
	DirectorID string
	Username   string
	ExpiresAt  time.Time
}

// Store is the persistence the login handlers need.
type Store interface {
	DirectorByUsername(ctx context.Context, username string) (Director, error)
	CreateSession(ctx context.Context, directorID string, credentialHash []byte, now, expiresAt time.Time) error
	SessionByHash(ctx context.Context, credentialHash []byte) (Session, error)
	DeleteSession(ctx context.Context, credentialHash []byte) error
}

// NormalizeUsername is applied wherever a username is stored or looked up,
// so "Alice" and "alice " are the same account.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// PGStore is the PostgreSQL Store.
type PGStore struct {
	Pool *pgxpool.Pool
}

var _ Store = (*PGStore)(nil)

// CreateDirector adds an account and returns its ID.
func (s *PGStore) CreateDirector(ctx context.Context, username, passwordHash string, now time.Time) (string, error) {
	id := uuid.NewString()
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO directors (id, username, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
			id, NormalizeUsername(username), passwordHash, now)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return "", ErrUsernameTaken
	}
	return id, err
}

func (s *PGStore) DirectorByUsername(ctx context.Context, username string) (Director, error) {
	var d Director
	err := s.Pool.QueryRow(ctx,
		`SELECT id, username, password_hash FROM directors WHERE username = $1`,
		NormalizeUsername(username)).Scan(&d.ID, &d.Username, &d.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

func (s *PGStore) CreateSession(ctx context.Context, directorID string, credentialHash []byte, now, expiresAt time.Time) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO director_sessions (id, director_id, credential_hash, created_at, expires_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			uuid.NewString(), directorID, credentialHash, now, expiresAt)
		return err
	})
}

func (s *PGStore) SessionByHash(ctx context.Context, credentialHash []byte) (Session, error) {
	var sess Session
	err := s.Pool.QueryRow(ctx,
		`SELECT d.id, d.username, s.expires_at
		   FROM director_sessions s
		   JOIN directors d ON d.id = s.director_id
		  WHERE s.credential_hash = $1`,
		credentialHash).Scan(&sess.DirectorID, &sess.Username, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, ErrNotFound
	}
	return sess, err
}

func (s *PGStore) DeleteSession(ctx context.Context, credentialHash []byte) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM director_sessions WHERE credential_hash = $1`, credentialHash)
		return err
	})
}

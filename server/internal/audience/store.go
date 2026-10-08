// Package audience handles anonymous audience sessions: joining a room
// through its share link, and recognising a returning browser.
package audience

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

// MaxPerRoom is how many audience sessions a room admits. Slots are never
// released (docs/decisions.md, D028).
const MaxPerRoom = 10

var (
	// ErrNotFound means no session has that credential.
	ErrNotFound = errors.New("audience session not found")
	// ErrRoomFull means the room has already admitted MaxPerRoom sessions.
	ErrRoomFull = errors.New("room is full")
	// ErrRoomNotFound means the room does not exist.
	ErrRoomNotFound = errors.New("room not found")
)

// Session is an audience member's session in one room.
type Session struct {
	ID        string
	RoomID    string
	ExpiresAt time.Time
}

// Store is the persistence the audience handlers need.
type Store interface {
	// Admit takes one of the room's slots and creates a session, or returns
	// ErrRoomFull or ErrRoomNotFound.
	Admit(ctx context.Context, roomID string, credentialHash []byte, now, expiresAt time.Time) (sessionID string, err error)
	SessionByHash(ctx context.Context, credentialHash []byte) (Session, error)
	// Touch records that a returning session was seen.
	Touch(ctx context.Context, sessionID string, now time.Time) error
}

// PGStore is the PostgreSQL Store.
type PGStore struct {
	Pool *pgxpool.Pool
}

var _ Store = (*PGStore)(nil)

// Admit claims a slot and inserts the session in one transaction.
//
// The claim is a conditional update of the room's counter, not "count the
// sessions, then insert": two browsers arriving together could both count
// nine and both insert. On PostgreSQL the update's row lock makes them queue;
// on Aurora DSQL they conflict at commit and db.WithTx retries one of them
// (docs/decisions.md, D032). Either way the eleventh sees a full room.
func (s *PGStore) Admit(ctx context.Context, roomID string, credentialHash []byte, now, expiresAt time.Time) (string, error) {
	var id string
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		id = uuid.NewString()
		claimed, err := tx.Exec(ctx,
			`UPDATE rooms SET audience_count = audience_count + 1 WHERE id = $1 AND audience_count < $2`,
			roomID, MaxPerRoom)
		if err != nil {
			return err
		}
		if claimed.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, roomID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrRoomNotFound
			}
			return ErrRoomFull
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO audience_sessions (id, room_id, credential_hash, created_at, last_seen_at, expires_at)
			 VALUES ($1, $2, $3, $4, $4, $5)`,
			id, roomID, credentialHash, now, expiresAt)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *PGStore) SessionByHash(ctx context.Context, credentialHash []byte) (Session, error) {
	var sess Session
	err := s.Pool.QueryRow(ctx,
		`SELECT id, room_id, expires_at FROM audience_sessions WHERE credential_hash = $1`,
		credentialHash).Scan(&sess.ID, &sess.RoomID, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, ErrNotFound
	}
	return sess, err
}

func (s *PGStore) Touch(ctx context.Context, sessionID string, now time.Time) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE audience_sessions SET last_seen_at = $2 WHERE id = $1`, sessionID, now)
		return err
	})
}

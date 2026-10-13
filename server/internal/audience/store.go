// Package audience handles anonymous audience sessions: joining a room
// through its share link, and recognising a returning browser.
package audience

import (
	"context"
	"encoding/hex"
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
	Admit(ctx context.Context, roomID string, credentialHash []byte, nickname string, now, expiresAt time.Time) (sessionID string, err error)
	SessionByHash(ctx context.Context, credentialHash []byte) (Session, error)
	// Touch records that a returning session was seen.
	Touch(ctx context.Context, sessionID string, now time.Time) error
	// Return records that a session entered again, under the nickname it
	// gave this time.
	Return(ctx context.Context, sessionID, nickname string, now time.Time) error
	// Nicknames lists a room's audience in the order they joined.
	Nicknames(ctx context.Context, roomID string) ([]string, error)
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
func (s *PGStore) Admit(ctx context.Context, roomID string, credentialHash []byte, nickname string, now, expiresAt time.Time) (string, error) {
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
			`INSERT INTO audience_sessions (id, room_id, credential_hash, nickname, created_at, last_seen_at, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $5, $6)`,
			id, roomID, hashText(credentialHash), nickname, now, expiresAt)
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
		hashText(credentialHash)).Scan(&sess.ID, &sess.RoomID, &sess.ExpiresAt)
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

func (s *PGStore) Return(ctx context.Context, sessionID, nickname string, now time.Time) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE audience_sessions SET nickname = $2, last_seen_at = $3 WHERE id = $1`, sessionID, nickname, now)
		return err
	})
}

// Nicknames is ordered by when each session was created, so a name keeps
// its place when its owner changes it.
func (s *PGStore) Nicknames(ctx context.Context, roomID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT nickname FROM audience_sessions WHERE room_id = $1 ORDER BY created_at, id`, roomID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// hashText is how a credential hash is stored: as its hex. The column is
// unique, so it is indexed, and Aurora DSQL cannot index bytea
// (docs/decisions.md, D047).
func hashText(credentialHash []byte) string {
	return hex.EncodeToString(credentialHash)
}

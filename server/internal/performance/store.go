package performance

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

// Statuses of a performance row.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// ErrNotFound means the room has no performance.
var ErrNotFound = errors.New("performance not found")

// uniqueViolation is the SQLSTATE for a broken unique constraint.
const uniqueViolation = "23505"

// Performance is a room's one performance, as stored. The script and audio
// keys are read separately, by the step that serves them.
type Performance struct {
	ID        string
	RoomID    string
	Status    string
	CreatedAt time.Time
}

// PGStore is the PostgreSQL store of performance rows.
type PGStore struct {
	Pool *pgxpool.Pool
}

// CreateOrGet returns the room's performance, creating it as running if the
// room has none. created says which happened.
//
// The table allows one row per room, which is what makes two simultaneous
// Starts safe. On PostgreSQL the loser's insert fails with a unique
// violation and it reads the winner's row; on Aurora DSQL the loser's
// transaction conflicts at commit, db.WithTx retries it, and the retry finds
// the row (docs/decisions.md, D032).
func (s *PGStore) CreateOrGet(ctx context.Context, roomID string, now time.Time) (p Performance, created bool, err error) {
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		created = false
		p, err = byRoom(ctx, tx, roomID)
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		p = Performance{ID: uuid.NewString(), RoomID: roomID, Status: StatusRunning, CreatedAt: now}
		_, err = tx.Exec(ctx,
			`INSERT INTO performances (id, room_id, status, created_at) VALUES ($1, $2, $3, $4)`,
			p.ID, p.RoomID, p.Status, p.CreatedAt)
		created = err == nil
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		p, err = s.ByRoom(ctx, roomID)
		return p, false, err
	}
	return p, created, err
}

// ByRoom returns the room's performance, or ErrNotFound.
func (s *PGStore) ByRoom(ctx context.Context, roomID string) (Performance, error) {
	return byRoom(ctx, s.Pool, roomID)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func byRoom(ctx context.Context, q querier, roomID string) (Performance, error) {
	var p Performance
	err := q.QueryRow(ctx,
		`SELECT id, room_id, status, created_at FROM performances WHERE room_id = $1`, roomID,
	).Scan(&p.ID, &p.RoomID, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Complete saves the final script and audio keys and marks the performance
// completed. Only a running row changes, so repeating it does nothing.
func (s *PGStore) Complete(ctx context.Context, id string, script Script, audioKeys []string, now time.Time) error {
	scriptJSON, err := json.Marshal(script)
	if err != nil {
		return err
	}
	keysJSON, err := json.Marshal(audioKeys)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE performances
			   SET status = $2, script = $3, audio_keys = $4, completed_at = $5
			 WHERE id = $1 AND status = $6`,
			id, StatusCompleted, scriptJSON, keysJSON, now, StatusRunning)
		return err
	})
}

// Fail marks the performance failed and records why. Only a running row
// changes, so repeating it does nothing and a completed run is never undone.
func (s *PGStore) Fail(ctx context.Context, id, reason string, now time.Time) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE performances
			   SET status = $2, failure_reason = $3, completed_at = $4
			 WHERE id = $1 AND status = $5`,
			id, StatusFailed, reason, now, StatusRunning)
		return err
	})
}

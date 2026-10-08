package rooms

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
)

// ErrNotFound means no room has that ID.
var ErrNotFound = errors.New("room not found")

// Store is the persistence the room handlers need.
type Store interface {
	Create(ctx context.Context, room Room) error
	Get(ctx context.Context, id string) (Room, error)
	ListByDirector(ctx context.Context, directorID string) ([]Summary, error)
}

// PGStore is the PostgreSQL Store.
type PGStore struct {
	Pool *pgxpool.Pool
}

var _ Store = (*PGStore)(nil)

func (s *PGStore) Create(ctx context.Context, r Room) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO rooms (
				id, director_id, scene_topic, scene_description, scene_genre,
				character_a_name, character_a_brief, character_a_voice_id, character_a_portrait_key,
				character_b_name, character_b_brief, character_b_voice_id, character_b_portrait_key,
				audience_count, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 0, $14)`,
			r.ID, r.DirectorID, r.Scene.Topic, r.Scene.Description, r.Scene.Genre,
			r.CharacterA.Name, r.CharacterA.Brief, r.CharacterA.VoiceID, r.CharacterA.PortraitKey,
			r.CharacterB.Name, r.CharacterB.Brief, r.CharacterB.VoiceID, r.CharacterB.PortraitKey,
			r.CreatedAt)
		return err
	})
}

func (s *PGStore) Get(ctx context.Context, id string) (Room, error) {
	var r Room
	err := s.Pool.QueryRow(ctx, `
		SELECT id, director_id, scene_topic, scene_description, scene_genre,
		       character_a_name, character_a_brief, character_a_voice_id, character_a_portrait_key,
		       character_b_name, character_b_brief, character_b_voice_id, character_b_portrait_key,
		       audience_count, created_at
		  FROM rooms WHERE id = $1`, id).Scan(
		&r.ID, &r.DirectorID, &r.Scene.Topic, &r.Scene.Description, &r.Scene.Genre,
		&r.CharacterA.Name, &r.CharacterA.Brief, &r.CharacterA.VoiceID, &r.CharacterA.PortraitKey,
		&r.CharacterB.Name, &r.CharacterB.Brief, &r.CharacterB.VoiceID, &r.CharacterB.PortraitKey,
		&r.AudienceCount, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *PGStore) ListByDirector(ctx context.Context, directorID string) ([]Summary, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, scene_topic, created_at FROM rooms WHERE director_id = $1 ORDER BY created_at DESC, id`,
		directorID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		err := row.Scan(&s.ID, &s.Topic, &s.CreatedAt)
		return s, err
	})
}

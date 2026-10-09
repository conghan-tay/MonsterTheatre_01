-- +goose Up
CREATE TABLE performances (
    id           uuid        PRIMARY KEY,
    room_id      uuid        NOT NULL UNIQUE REFERENCES rooms (id),
    status       text        NOT NULL,
    script       jsonb,
    audio_keys   jsonb,
    created_at   timestamptz NOT NULL,
    completed_at timestamptz
);

-- +goose Down
DROP TABLE performances;

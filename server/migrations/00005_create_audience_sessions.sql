-- +goose Up
CREATE TABLE audience_sessions (
    id              uuid        PRIMARY KEY,
    room_id         uuid        NOT NULL REFERENCES rooms (id),
    credential_hash bytea       NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);

-- +goose Down
DROP TABLE audience_sessions;

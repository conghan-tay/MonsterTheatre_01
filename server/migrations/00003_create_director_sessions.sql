-- +goose Up
CREATE TABLE director_sessions (
    id              uuid        PRIMARY KEY,
    director_id     uuid        NOT NULL REFERENCES directors (id),
    credential_hash bytea       NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);

-- +goose Down
DROP TABLE director_sessions;

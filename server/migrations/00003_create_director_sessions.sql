-- +goose Up
CREATE TABLE director_sessions (
    id              uuid        PRIMARY KEY,
    director_id     uuid        NOT NULL REFERENCES directors (id),
    -- SHA-256 of the cookie's secret, as hex. Text, not bytea: the column is
    -- unique, so it is indexed, and Aurora DSQL cannot index bytea.
    credential_hash text        NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);

-- +goose Down
DROP TABLE director_sessions;

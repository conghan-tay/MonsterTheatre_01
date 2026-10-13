-- +goose Up
CREATE TABLE audience_sessions (
    id              uuid        PRIMARY KEY,
    room_id         uuid        NOT NULL REFERENCES rooms (id),
    -- SHA-256 of the cookie's secret, as hex. Text, not bytea: the column is
    -- unique, so it is indexed, and Aurora DSQL cannot index bytea.
    credential_hash text        NOT NULL UNIQUE,
    -- What this person asked to be called. Shown to the room's director.
    nickname        text        NOT NULL,
    created_at      timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);

-- +goose Down
DROP TABLE audience_sessions;

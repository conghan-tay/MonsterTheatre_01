-- +goose Up
CREATE TABLE directors (
    id            uuid        PRIMARY KEY,
    username      text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL
);

-- +goose Down
DROP TABLE directors;

-- +goose Up
CREATE TABLE rooms (
    id                       uuid        PRIMARY KEY,
    director_id              uuid        NOT NULL REFERENCES directors (id),
    scene_topic              text        NOT NULL,
    scene_description        text        NOT NULL,
    scene_genre              text        NOT NULL,
    character_a_name         text        NOT NULL,
    character_a_brief        text        NOT NULL,
    character_a_voice_id     text        NOT NULL,
    character_a_portrait_key text        NOT NULL,
    character_b_name         text        NOT NULL,
    character_b_brief        text        NOT NULL,
    character_b_voice_id     text        NOT NULL,
    character_b_portrait_key text        NOT NULL,
    audience_count           integer     NOT NULL,
    created_at               timestamptz NOT NULL
);

-- +goose Down
DROP TABLE rooms;

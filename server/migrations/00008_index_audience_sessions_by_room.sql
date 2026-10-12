-- +goose Up
-- For listing a room's audience in the order they joined, which is read on
-- every join. On Aurora DSQL this statement is CREATE INDEX ASYNC.
CREATE INDEX audience_sessions_room_id_created_at_idx ON audience_sessions (room_id, created_at);

-- +goose Down
DROP INDEX audience_sessions_room_id_created_at_idx;

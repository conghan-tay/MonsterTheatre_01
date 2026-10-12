-- +goose Up
-- For listing a director's rooms by when they were created. On Aurora DSQL
-- this statement is CREATE INDEX ASYNC.
CREATE INDEX rooms_director_id_created_at_idx ON rooms (director_id, created_at);

-- +goose Down
DROP INDEX rooms_director_id_created_at_idx;

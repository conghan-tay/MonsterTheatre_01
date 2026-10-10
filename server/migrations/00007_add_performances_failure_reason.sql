-- +goose Up
ALTER TABLE performances ADD COLUMN failure_reason text;

-- +goose Down
ALTER TABLE performances DROP COLUMN failure_reason;

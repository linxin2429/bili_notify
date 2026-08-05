-- +goose Up
ALTER TABLE ups ADD COLUMN exclusive_baseline_ready INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ups DROP COLUMN exclusive_baseline_ready;

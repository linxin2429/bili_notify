-- +goose Up
ALTER TABLE ai_profiles ADD COLUMN is_enabled INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE ai_profiles DROP COLUMN is_enabled;

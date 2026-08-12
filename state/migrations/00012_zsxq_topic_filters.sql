-- +goose Up
ALTER TABLE sources ADD COLUMN zsxq_topic_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE sources ADD COLUMN zsxq_authors_json TEXT NOT NULL DEFAULT '[]';
UPDATE sources SET zsxq_topic_mode = 'all' WHERE platform = 'zsxq';

-- +goose Down
ALTER TABLE sources DROP COLUMN zsxq_authors_json;
ALTER TABLE sources DROP COLUMN zsxq_topic_mode;

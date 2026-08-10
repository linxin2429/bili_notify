-- +goose Up
ALTER TABLE dynamics ADD COLUMN bvid TEXT NOT NULL DEFAULT '';

ALTER TABLE ai_jobs ADD COLUMN origin TEXT NOT NULL DEFAULT 'workbench';
ALTER TABLE ai_jobs ADD COLUMN source_dynamic_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_jobs ADD COLUMN depends_on_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_jobs ADD COLUMN origin_traceparent TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_jobs ADD COLUMN origin_tracestate TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_jobs ADD COLUMN target_channel_ids TEXT NOT NULL DEFAULT '[]';

CREATE INDEX idx_ai_jobs_dynamic ON ai_jobs(source_dynamic_id, created_at);
CREATE INDEX idx_ai_jobs_dependency ON ai_jobs(depends_on_job_id, state);

-- +goose Down
DROP INDEX IF EXISTS idx_ai_jobs_dependency;
DROP INDEX IF EXISTS idx_ai_jobs_dynamic;
ALTER TABLE ai_jobs DROP COLUMN target_channel_ids;
ALTER TABLE ai_jobs DROP COLUMN origin_tracestate;
ALTER TABLE ai_jobs DROP COLUMN origin_traceparent;
ALTER TABLE ai_jobs DROP COLUMN depends_on_job_id;
ALTER TABLE ai_jobs DROP COLUMN source_dynamic_id;
ALTER TABLE ai_jobs DROP COLUMN origin;
ALTER TABLE dynamics DROP COLUMN bvid;

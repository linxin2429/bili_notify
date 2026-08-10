-- +goose Up
CREATE TABLE ai_profiles (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  name       TEXT NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0,
  sealed     BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(kind, name)
);
CREATE INDEX idx_ai_profiles_kind ON ai_profiles(kind, is_default DESC, name);

CREATE TABLE ai_prompt_templates (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  is_default INTEGER NOT NULL DEFAULT 0,
  sealed     BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_ai_prompts_default ON ai_prompt_templates(is_default DESC, name);

CREATE TABLE ai_jobs (
  id                TEXT PRIMARY KEY,
  client_request_id TEXT NOT NULL UNIQUE,
  kind              TEXT NOT NULL,
  state             TEXT NOT NULL,
  stage             TEXT NOT NULL DEFAULT '',
  progress          INTEGER NOT NULL DEFAULT 0,
  profile_id        TEXT NOT NULL,
  prompt_id         TEXT NOT NULL DEFAULT '',
  attempts          INTEGER NOT NULL DEFAULT 0,
  error_code        TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',
  input_sealed      BLOB NOT NULL,
  config_sealed     BLOB NOT NULL,
  result_sealed     BLOB,
  created_at        INTEGER NOT NULL,
  started_at        INTEGER,
  finished_at       INTEGER,
  updated_at        INTEGER NOT NULL,
  FOREIGN KEY(profile_id) REFERENCES ai_profiles(id) ON DELETE RESTRICT
);
CREATE INDEX idx_ai_jobs_queue ON ai_jobs(state, kind, created_at);
CREATE INDEX idx_ai_jobs_history ON ai_jobs(created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS ai_jobs;
DROP TABLE IF EXISTS ai_prompt_templates;
DROP TABLE IF EXISTS ai_profiles;

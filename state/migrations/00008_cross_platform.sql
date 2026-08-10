-- +goose Up
CREATE TABLE platform_accounts (
  platform          TEXT PRIMARY KEY CHECK(platform IN ('bilibili', 'zsxq')),
  external_id       TEXT NOT NULL DEFAULT '',
  display_name      TEXT NOT NULL DEFAULT '',
  masked_phone      TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'disconnected',
  sealed_session    BLOB,
  sealed_aad        TEXT NOT NULL DEFAULT 'platform_accounts',
  verified_at       INTEGER,
  updated_at        INTEGER NOT NULL,
  risk_paused_until INTEGER,
  last_error        TEXT NOT NULL DEFAULT ''
);

INSERT INTO platform_accounts(platform, status, sealed_session, sealed_aad, updated_at)
SELECT 'bilibili', 'connected', sealed, 'auth_session', CAST(strftime('%s','now') AS INTEGER)
FROM auth_session WHERE id = 'session';

CREATE TABLE sources (
  id                TEXT PRIMARY KEY,
  platform          TEXT NOT NULL CHECK(platform IN ('bilibili', 'zsxq')),
  type              TEXT NOT NULL CHECK(type IN ('up', 'planet')),
  external_id       TEXT NOT NULL,
  name              TEXT NOT NULL DEFAULT '',
  note              TEXT NOT NULL DEFAULT '',
  owner_id          TEXT NOT NULL DEFAULT '',
  owner_name        TEXT NOT NULL DEFAULT '',
  enabled           INTEGER NOT NULL DEFAULT 0,
  baseline_state    TEXT NOT NULL DEFAULT 'pending',
  backfill_cursor   TEXT NOT NULL DEFAULT '',
  high_watermark    TEXT NOT NULL DEFAULT '',
  backfill_done     INTEGER NOT NULL DEFAULT 0,
  backfill_total    INTEGER NOT NULL DEFAULT 0,
  last_poll_at      INTEGER,
  last_success_at   INTEGER,
  last_comment_at   INTEGER,
  sync_lag_sec      INTEGER NOT NULL DEFAULT 0,
  last_error        TEXT NOT NULL DEFAULT '',
  consecutive_fails INTEGER NOT NULL DEFAULT 0,
  UNIQUE(platform, type, external_id)
);
CREATE INDEX idx_sources_platform_enabled ON sources(platform, enabled);

INSERT INTO sources(id, platform, type, external_id, name, enabled, baseline_state,
                    last_poll_at, last_success_at, last_error, consecutive_fails)
SELECT 'bilibili:up:' || uid, 'bilibili', 'up', uid, name, enabled,
       CASE WHEN baseline_ready != 0 THEN 'complete' ELSE 'pending' END,
       last_poll_at, last_success_at, last_error, consecutive_fail
FROM ups;

CREATE TABLE contents (
  id              TEXT PRIMARY KEY,
  platform        TEXT NOT NULL CHECK(platform IN ('bilibili', 'zsxq')),
  source_id       TEXT NOT NULL,
  external_id     TEXT NOT NULL,
  author_id       TEXT NOT NULL DEFAULT '',
  author_name     TEXT NOT NULL DEFAULT '',
  upstream_type   TEXT NOT NULL,
  type            TEXT NOT NULL,
  title           TEXT NOT NULL DEFAULT '',
  body_text       TEXT NOT NULL DEFAULT '',
  safe_html       TEXT NOT NULL DEFAULT '',
  url             TEXT NOT NULL DEFAULT '',
  published_at    INTEGER NOT NULL,
  upstream_updated_at INTEGER,
  first_seen_at   INTEGER NOT NULL,
  last_synced_at  INTEGER NOT NULL,
  deleted_at      INTEGER,
  stats_json      TEXT NOT NULL DEFAULT '{}',
  tree_incomplete INTEGER NOT NULL DEFAULT 0,
  baseline        INTEGER NOT NULL DEFAULT 0,
  search_text     TEXT NOT NULL DEFAULT '',
  raw_json        TEXT NOT NULL DEFAULT '{}',
  FOREIGN KEY(source_id) REFERENCES sources(id) ON DELETE CASCADE
);
CREATE INDEX idx_contents_source_pub ON contents(source_id, published_at DESC, id DESC);
CREATE INDEX idx_contents_platform_pub ON contents(platform, published_at DESC, id DESC);

INSERT INTO contents(id, platform, source_id, external_id, author_id, author_name,
                     upstream_type, type, title, body_text, url, published_at,
                     first_seen_at, last_synced_at, baseline, search_text, raw_json)
SELECT 'bilibili:content:' || id, 'bilibili', 'bilibili:up:' || uid, id, uid, up_name,
       type,
       CASE WHEN bvid != '' THEN 'video' WHEN type LIKE '%ARTICLE%' THEN 'article' ELSE 'dynamic' END,
       title, CASE WHEN description != '' THEN description ELSE summary END, url,
       published_at, discovered_at, discovered_at, baseline, search_text,
       json_set(payload_json, '$.id', 'bilibili:content:' || id)
FROM dynamics;

UPDATE ai_jobs SET source_dynamic_id = 'bilibili:content:' || source_dynamic_id
WHERE source_dynamic_id != '' AND source_dynamic_id NOT LIKE 'bilibili:content:%';

CREATE TABLE attachments (
  id             TEXT PRIMARY KEY,
  content_id     TEXT NOT NULL,
  external_id    TEXT NOT NULL,
  type           TEXT NOT NULL CHECK(type IN ('image', 'file', 'audio', 'video', 'link')),
  file_name      TEXT NOT NULL DEFAULT '',
  mime           TEXT NOT NULL DEFAULT '',
  size           INTEGER NOT NULL DEFAULT 0,
  width          INTEGER NOT NULL DEFAULT 0,
  height         INTEGER NOT NULL DEFAULT 0,
  duration_sec   INTEGER NOT NULL DEFAULT 0,
  remote_url     TEXT NOT NULL DEFAULT '',
  remote_host    TEXT NOT NULL DEFAULT '',
  local_path     TEXT NOT NULL DEFAULT '',
  localize_error TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(content_id) REFERENCES contents(id) ON DELETE CASCADE,
  UNIQUE(content_id, external_id)
);
CREATE INDEX idx_attachments_content ON attachments(content_id);

INSERT OR IGNORE INTO attachments(
  id, content_id, external_id, type, mime, size, width, height,
  remote_url, local_path
)
SELECT
  content.id || ':attachment:media-' || media.key,
  content.id,
  'media-' || media.key,
  'image',
  COALESCE(json_extract(media.value, '$.content_type'), ''),
  COALESCE(json_extract(media.value, '$.size'), 0),
  COALESCE(json_extract(media.value, '$.width'), 0),
  COALESCE(json_extract(media.value, '$.height'), 0),
  COALESCE(json_extract(media.value, '$.url'), ''),
  COALESCE(json_extract(media.value, '$.local_path'), '')
FROM dynamics d
JOIN contents content ON content.id = 'bilibili:content:' || d.id
JOIN json_each(d.payload_json, '$.media') media;

CREATE TABLE comment_nodes (
  id              TEXT PRIMARY KEY,
  platform        TEXT NOT NULL CHECK(platform IN ('bilibili', 'zsxq')),
  content_id      TEXT NOT NULL,
  external_id     TEXT NOT NULL,
  root_id         TEXT NOT NULL DEFAULT '',
  parent_id       TEXT NOT NULL DEFAULT '',
  author_id       TEXT NOT NULL DEFAULT '',
  author_name     TEXT NOT NULL DEFAULT '',
  author_role     TEXT NOT NULL DEFAULT 'member',
  body_text       TEXT NOT NULL DEFAULT '',
  media_json      TEXT NOT NULL DEFAULT '[]',
  published_at    INTEGER NOT NULL,
  upstream_updated_at INTEGER,
  first_seen_at   INTEGER NOT NULL,
  deleted_at      INTEGER,
  baseline        INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(content_id) REFERENCES contents(id) ON DELETE CASCADE,
  UNIQUE(platform, external_id)
);
CREATE INDEX idx_comment_nodes_content_time ON comment_nodes(content_id, published_at, id);
CREATE INDEX idx_comment_nodes_parent ON comment_nodes(content_id, parent_id);

-- Legacy comment rows stored only notifications, but each notification carries
-- the complete known ancestor thread. Expand those arrays and deduplicate nodes
-- so upgrades retain the already archived tree instead of starting over.
INSERT OR IGNORE INTO comment_nodes(
  id, platform, content_id, external_id, root_id, parent_id,
  author_id, author_name, author_role, body_text, media_json,
  published_at, first_seen_at, baseline
)
SELECT
  'bilibili:comment:' || json_extract(node.value, '$.rpid'),
  'bilibili',
  'bilibili:content:' || c.content_id,
  json_extract(node.value, '$.rpid'),
  CASE
    WHEN COALESCE(json_extract(node.value, '$.parent'), '') IN ('', '0')
      THEN 'bilibili:comment:' || json_extract(node.value, '$.rpid')
    ELSE 'bilibili:comment:' || json_extract(node.value, '$.parent')
  END,
  CASE
    WHEN COALESCE(json_extract(node.value, '$.parent'), '') IN ('', '0') THEN ''
    ELSE 'bilibili:comment:' || json_extract(node.value, '$.parent')
  END,
  COALESCE(json_extract(node.value, '$.mid'), ''),
  COALESCE(json_extract(node.value, '$.name'), ''),
  CASE WHEN COALESCE(json_extract(node.value, '$.is_up'), 0) != 0 THEN 'up' ELSE 'member' END,
  COALESCE(json_extract(node.value, '$.message'), ''),
  '[]',
  COALESCE(CAST(strftime('%s', json_extract(node.value, '$.time')) AS INTEGER), c.published_at),
  c.discovered_at,
  c.baseline
FROM comments c
JOIN contents content ON content.id = 'bilibili:content:' || c.content_id
JOIN json_each(c.payload_json, '$.thread') node
WHERE COALESCE(json_extract(node.value, '$.rpid'), '') != '';

CREATE TABLE sync_targets (
  platform       TEXT NOT NULL,
  content_id     TEXT NOT NULL,
  baseline_ready INTEGER NOT NULL DEFAULT 0,
  last_synced_at INTEGER,
  last_error     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(platform, content_id),
  FOREIGN KEY(content_id) REFERENCES contents(id) ON DELETE CASCADE
);

INSERT INTO sync_targets(platform, content_id, baseline_ready, last_synced_at, last_error)
SELECT 'bilibili', 'bilibili:content:' || dynamic_id, baseline_ready, last_poll_at, last_error
FROM comment_targets;

CREATE TABLE seen_items (
  platform    TEXT NOT NULL,
  source_id   TEXT NOT NULL,
  entity_type TEXT NOT NULL CHECK(entity_type IN ('content', 'comment')),
  entity_id   TEXT NOT NULL,
  first_seen_at INTEGER NOT NULL,
  PRIMARY KEY(platform, source_id, entity_type, entity_id)
);

INSERT OR IGNORE INTO seen_items(platform, source_id, entity_type, entity_id, first_seen_at)
SELECT 'bilibili', 'bilibili:up:' || uid, 'content', 'bilibili:content:' || dynamic_id, seen_at
FROM seen_dynamics;

INSERT OR IGNORE INTO seen_items(platform, source_id, entity_type, entity_id, first_seen_at)
SELECT 'bilibili', 'bilibili:up:' || uid, 'comment', 'bilibili:comment:' || rpid, seen_at
FROM seen_comments;

CREATE TABLE outbox (
  id                 TEXT PRIMARY KEY,
  kind               TEXT NOT NULL CHECK(kind IN ('content', 'comment_digest', 'ai')),
  platform           TEXT NOT NULL DEFAULT '',
  source_id          TEXT NOT NULL DEFAULT '',
  content_id         TEXT NOT NULL DEFAULT '',
  channel_id         TEXT NOT NULL,
  idempotency_key    TEXT NOT NULL,
  state              TEXT NOT NULL,
  attempts           INTEGER NOT NULL DEFAULT 0,
  next_at            INTEGER NOT NULL,
  last_error         TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  payload_json       TEXT NOT NULL,
  progress_json      TEXT NOT NULL DEFAULT '{}',
  origin_traceparent TEXT NOT NULL DEFAULT '',
  UNIQUE(channel_id, idempotency_key)
);
CREATE INDEX idx_outbox_due ON outbox(state, next_at);
CREATE INDEX idx_outbox_content ON outbox(content_id, created_at);

UPDATE meta
SET value = json_remove(json_set(value,
  '$._version', 3,
  '$.bilibili_dynamic_interval_sec', json_extract(value, '$.poll_interval_sec'),
  '$.bilibili_request_rate', json_extract(value, '$.request_rate'),
  '$.bilibili_request_concurrency', json_extract(value, '$.request_concurrency'),
  '$.bilibili_comments_enabled', json_extract(value, '$.comment_enabled'),
  '$.bilibili_comment_track_n', json_extract(value, '$.comment_track_n'),
  '$.bilibili_comment_interval_sec', json_extract(value, '$.comment_batch_interval_sec'),
  '$.bilibili_relation_refresh_interval_sec', json_extract(value, '$.relation_refresh_interval_sec'),
  '$.bilibili_space_reconcile_interval_sec', json_extract(value, '$.space_reconcile_interval_sec'),
  '$.bilibili_max_dynamic_pages', json_extract(value, '$.max_dynamic_pages'),
  '$.bilibili_risk_pause_sec', json_extract(value, '$.risk_pause_sec'),
  '$.zsxq_dynamic_interval_sec', 60,
  '$.zsxq_comment_interval_sec', 600,
  '$.zsxq_comments_enabled', json('true'),
  '$.zsxq_request_rate', 1.0,
  '$.zsxq_request_concurrency', 2,
  '$.zsxq_risk_pause_sec', 600,
  '$.zsxq_asset_max_file_mib', 500,
  '$.zsxq_asset_total_budget_gib', 50),
  '$.poll_interval_sec', '$.request_rate', '$.request_concurrency', '$.comment_enabled',
  '$.comment_track_n', '$.comment_root_pages', '$.comment_reply_pages', '$.comment_batch_interval_sec',
  '$.relation_refresh_interval_sec', '$.space_reconcile_interval_sec', '$.max_dynamic_pages', '$.risk_pause_sec')
WHERE key = 'runtime_settings';

-- +goose Down
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS seen_items;
DROP TABLE IF EXISTS sync_targets;
DROP TABLE IF EXISTS comment_nodes;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS contents;
DROP TABLE IF EXISTS sources;
DROP TABLE IF EXISTS platform_accounts;

-- +goose Up
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE ups (
  uid              TEXT PRIMARY KEY,
  name             TEXT NOT NULL DEFAULT '',
  enabled          INTEGER NOT NULL DEFAULT 0,
  baseline_ready   INTEGER NOT NULL DEFAULT 0,
  last_poll_at     INTEGER,
  last_success_at  INTEGER,
  last_error       TEXT NOT NULL DEFAULT '',
  consecutive_fail INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE channels (
  id     TEXT PRIMARY KEY,
  sealed BLOB NOT NULL
);

CREATE TABLE auth_session (
  id     TEXT PRIMARY KEY,
  sealed BLOB NOT NULL
);

CREATE TABLE seen_dynamics (
  uid        TEXT NOT NULL,
  dynamic_id TEXT NOT NULL,
  seen_at    INTEGER NOT NULL,
  PRIMARY KEY (uid, dynamic_id)
);
CREATE INDEX idx_seen_dyn_uid ON seen_dynamics(uid);

CREATE TABLE seen_comments (
  uid     TEXT NOT NULL,
  rpid    TEXT NOT NULL,
  seen_at INTEGER NOT NULL,
  PRIMARY KEY (uid, rpid)
);
CREATE INDEX idx_seen_cmt_uid ON seen_comments(uid);

CREATE TABLE comment_targets (
  uid            TEXT NOT NULL,
  comment_type   INTEGER NOT NULL,
  comment_oid    TEXT NOT NULL,
  up_name        TEXT NOT NULL DEFAULT '',
  dynamic_id     TEXT NOT NULL DEFAULT '',
  content_type   TEXT NOT NULL DEFAULT '',
  title          TEXT NOT NULL DEFAULT '',
  url            TEXT NOT NULL DEFAULT '',
  published_at   INTEGER NOT NULL DEFAULT 0,
  comment_count  INTEGER NOT NULL DEFAULT 0,
  closed         INTEGER NOT NULL DEFAULT 0,
  baseline_ready INTEGER NOT NULL DEFAULT 0,
  last_poll_at   INTEGER,
  last_error     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (uid, comment_type, comment_oid)
);
CREATE INDEX idx_comment_targets_uid_pub ON comment_targets(uid, published_at DESC);

CREATE TABLE deliveries (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  channel_id   TEXT NOT NULL,
  state        TEXT NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_at      INTEGER NOT NULL,
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  payload_json TEXT NOT NULL
);
CREATE INDEX idx_deliveries_due ON deliveries(state, next_at);
CREATE INDEX idx_deliveries_channel ON deliveries(channel_id);

CREATE TABLE dynamics (
  id            TEXT PRIMARY KEY,
  uid           TEXT NOT NULL,
  up_name       TEXT NOT NULL,
  type          TEXT NOT NULL,
  published_at  INTEGER NOT NULL,
  discovered_at INTEGER NOT NULL,
  baseline      INTEGER NOT NULL DEFAULT 0,
  title         TEXT NOT NULL DEFAULT '',
  summary       TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  target_url    TEXT NOT NULL DEFAULT '',
  badge         TEXT NOT NULL DEFAULT '',
  search_text   TEXT NOT NULL,
  payload_json  TEXT NOT NULL
);
CREATE INDEX idx_dyn_pub ON dynamics(published_at DESC, id DESC);
CREATE INDEX idx_dyn_uid_pub ON dynamics(uid, published_at DESC, id DESC);

CREATE TABLE comments (
  rpid          TEXT PRIMARY KEY,
  up_uid        TEXT NOT NULL,
  up_name       TEXT NOT NULL,
  content_type  TEXT NOT NULL DEFAULT '',
  content_id    TEXT NOT NULL DEFAULT '',
  content_title TEXT NOT NULL DEFAULT '',
  content_url   TEXT NOT NULL DEFAULT '',
  published_at  INTEGER NOT NULL,
  discovered_at INTEGER NOT NULL,
  baseline      INTEGER NOT NULL DEFAULT 0,
  incomplete    INTEGER NOT NULL DEFAULT 0,
  search_text   TEXT NOT NULL,
  payload_json  TEXT NOT NULL
);
CREATE INDEX idx_cmt_pub ON comments(published_at DESC, rpid DESC);
CREATE INDEX idx_cmt_uid_pub ON comments(up_uid, published_at DESC, rpid DESC);

-- +goose Down
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS dynamics;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS comment_targets;
DROP TABLE IF EXISTS seen_comments;
DROP TABLE IF EXISTS seen_dynamics;
DROP TABLE IF EXISTS auth_session;
DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS ups;
DROP TABLE IF EXISTS meta;

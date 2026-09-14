-- +goose Up
CREATE TABLE collection_items (
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  payload BLOB NOT NULL,
  baseline_mode INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_at INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, id, kind)
);
CREATE INDEX idx_collection_items_due ON collection_items(kind, next_at);
CREATE TABLE collection_scans (
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  offset TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 0,
  baseline_mode INTEGER NOT NULL DEFAULT 0,
  seen_through INTEGER NOT NULL DEFAULT 0,
  pending_through INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_at INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE collection_feed_gaps (
  account_uid TEXT NOT NULL,
  id TEXT NOT NULL,
  payload BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(account_uid, id)
);

-- +goose Down
DROP TABLE collection_scans;
DROP TABLE collection_items;
DROP TABLE collection_feed_gaps;

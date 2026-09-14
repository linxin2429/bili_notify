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
CREATE INDEX idx_collection_feed_gaps_created ON collection_feed_gaps(created_at);

-- Diagnostic samples belong to the current Bilibili account. Keep cleanup
-- atomic with every account write, including direct platform-account APIs.
-- +goose StatementBegin
CREATE TRIGGER collection_feed_gaps_account_insert
AFTER INSERT ON platform_accounts WHEN NEW.platform = 'bilibili'
BEGIN
  DELETE FROM collection_feed_gaps WHERE account_uid != NEW.external_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER collection_feed_gaps_account_replace
AFTER UPDATE OF external_id ON platform_accounts
WHEN OLD.platform = 'bilibili' AND OLD.external_id != NEW.external_id
BEGIN
  DELETE FROM collection_feed_gaps;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER collection_feed_gaps_account_delete
AFTER DELETE ON platform_accounts WHEN OLD.platform = 'bilibili'
BEGIN
  DELETE FROM collection_feed_gaps;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER collection_feed_gaps_account_delete;
DROP TRIGGER collection_feed_gaps_account_replace;
DROP TRIGGER collection_feed_gaps_account_insert;
DROP TABLE collection_scans;
DROP TABLE collection_items;
DROP TABLE collection_feed_gaps;

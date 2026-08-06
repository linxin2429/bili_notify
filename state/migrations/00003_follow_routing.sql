-- +goose Up
CREATE TABLE bili_feed_state (
  account_uid     TEXT PRIMARY KEY,
  update_baseline TEXT NOT NULL DEFAULT '',
  initialized     INTEGER NOT NULL DEFAULT 0,
  updated_at      INTEGER NOT NULL
);

CREATE TABLE up_follow_relations (
  account_uid       TEXT NOT NULL,
  up_uid            TEXT NOT NULL,
  follow_state      TEXT NOT NULL DEFAULT 'unknown',
  space_synced      INTEGER NOT NULL DEFAULT 0,
  checked_at        INTEGER,
  last_space_poll_at INTEGER,
  PRIMARY KEY (account_uid, up_uid)
);
CREATE INDEX idx_up_follow_relations_up ON up_follow_relations(up_uid);

-- +goose Down
DROP TABLE IF EXISTS up_follow_relations;
DROP TABLE IF EXISTS bili_feed_state;

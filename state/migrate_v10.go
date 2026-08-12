package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/pressly/goose/v3"
)

const tableChannelSecrets = "channel_secrets"

// migrateV10 closes the v8 transition in one irreversible transaction.  It is
// intentionally a Go migration because encrypted records must be opened with
// the operator's vault before they can be rebound to their final AAD.
func migrateV10(v *vault.Vault) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		if err := reconcileV9Facts(ctx, tx); err != nil {
			return fmt.Errorf("reconciling legacy facts: %w", err)
		}
		if err := migrateV10Accounts(ctx, tx, v); err != nil {
			return fmt.Errorf("migrating accounts: %w", err)
		}
		if err := migrateV10Channels(ctx, tx, v); err != nil {
			return fmt.Errorf("migrating channels: %w", err)
		}
		if err := migrateV10SyncTargets(ctx, tx); err != nil {
			return fmt.Errorf("migrating comment sync targets: %w", err)
		}
		if err := migrateV10Outbox(ctx, tx); err != nil {
			return fmt.Errorf("migrating outbox: %w", err)
		}
		if err := migrateV10AIJobs(ctx, tx); err != nil {
			return fmt.Errorf("migrating AI jobs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE media_cleanup_tasks (
  id             TEXT PRIMARY KEY,
  relative_path  TEXT NOT NULL UNIQUE,
  state          TEXT NOT NULL CHECK(state IN ('pending', 'blocked')),
  attempts       INTEGER NOT NULL DEFAULT 0,
  next_at        INTEGER NOT NULL,
  last_error     TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX idx_media_cleanup_due ON media_cleanup_tasks(state, next_at, id);

DROP TABLE auth_session;
DROP TABLE deliveries;
DROP TABLE comments;
DROP TABLE dynamics;
DROP TABLE seen_comments;
DROP TABLE seen_dynamics;
DROP TABLE comment_targets;
DROP TABLE ups;
`); err != nil {
			return fmt.Errorf("dropping transitional tables: %w", err)
		}
		return nil
	}
}

func reconcileV9Facts(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
ALTER TABLE sources ADD COLUMN exclusive_baseline_ready INTEGER NOT NULL DEFAULT 0;
UPDATE sources
SET exclusive_baseline_ready = COALESCE((SELECT exclusive_baseline_ready FROM ups WHERE 'bilibili:up:' || ups.uid = sources.id), 0)
WHERE platform = 'bilibili';

INSERT OR IGNORE INTO sources(
  id, platform, type, external_id, name, enabled, baseline_state,
  last_poll_at, last_success_at, last_error, consecutive_fails
)
SELECT 'bilibili:up:' || uid, 'bilibili', 'up', uid, name, enabled,
       CASE WHEN baseline_ready != 0 THEN 'complete' ELSE 'pending' END,
       last_poll_at, last_success_at, last_error, consecutive_fail
FROM ups;

INSERT OR IGNORE INTO contents(
  id, platform, source_id, external_id, author_id, author_name,
  upstream_type, type, title, body_text, url, published_at,
  first_seen_at, last_synced_at, baseline, search_text, raw_json
)
SELECT 'bilibili:content:' || id, 'bilibili', 'bilibili:up:' || uid, id, uid, up_name,
       type,
       CASE WHEN bvid != '' THEN 'video' WHEN type LIKE '%ARTICLE%' THEN 'article' ELSE 'dynamic' END,
       title, CASE WHEN description != '' THEN description ELSE summary END, url,
       published_at, discovered_at, discovered_at, baseline, search_text,
       json_set(payload_json, '$.id', 'bilibili:content:' || id)
FROM dynamics;

INSERT OR IGNORE INTO seen_items(platform, source_id, entity_type, entity_id, first_seen_at)
SELECT 'bilibili', 'bilibili:up:' || uid, 'content', 'bilibili:content:' || dynamic_id, seen_at
FROM seen_dynamics;

INSERT OR IGNORE INTO seen_items(platform, source_id, entity_type, entity_id, first_seen_at)
SELECT 'bilibili', 'bilibili:up:' || uid, 'comment', 'bilibili:comment:' || rpid, seen_at
FROM seen_comments;
`)
	return err
}

func migrateV10Accounts(ctx context.Context, tx *sql.Tx, v *vault.Vault) error {
	rows, err := tx.QueryContext(ctx, `SELECT platform, external_id, display_name, sealed_session, sealed_aad FROM platform_accounts WHERE sealed_session IS NOT NULL`)
	if err != nil {
		return err
	}
	type accountSecret struct {
		platform, externalID, displayName, aad string
		sealed                                 []byte
	}
	var items []accountSecret
	for rows.Next() {
		var item accountSecret
		if err := rows.Scan(&item.platform, &item.externalID, &item.displayName, &item.sealed, &item.aad); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var session map[string]string
		if item.aad == tableAuthSession && item.platform == string(model.PlatformBilibili) {
			var legacy model.BiliSession
			if err := openJSON(v, tableAuthSession, authSessionID, item.sealed, &legacy); err != nil {
				return fmt.Errorf("opening legacy Bilibili session: %w", err)
			}
			session = legacy.Cookies
			if item.externalID == "" {
				item.externalID, item.displayName = legacy.AccountUID, legacy.AccountName
			}
		} else {
			if item.aad != tablePlatformAccounts {
				return fmt.Errorf("unrecognized account AAD %q for %s", item.aad, item.platform)
			}
			if err := openJSON(v, tablePlatformAccounts, item.platform, item.sealed, &session); err != nil {
				return fmt.Errorf("opening %s session: %w", item.platform, err)
			}
		}
		sealed, err := sealJSON(v, tablePlatformAccounts, item.platform, session)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_accounts SET external_id=?, display_name=?, sealed_session=?, sealed_aad='platform_accounts' WHERE platform=?`, item.externalID, item.displayName, sealed, item.platform); err != nil {
			return err
		}
	}
	return nil
}

func migrateV10Channels(ctx context.Context, tx *sql.Tx, v *vault.Vault) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE channels_v10 (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  type                 TEXT NOT NULL,
  enabled              INTEGER NOT NULL,
  public_settings_json TEXT NOT NULL DEFAULT '{}',
  secret_sealed        BLOB NOT NULL,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, sealed FROM channels ORDER BY id`)
	if err != nil {
		return err
	}
	type legacyChannel struct {
		id     string
		sealed []byte
	}
	var items []legacyChannel
	for rows.Next() {
		var item legacyChannel
		if err := rows.Scan(&item.id, &item.sealed); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var channel model.Channel
		if err := openJSON(v, tableChannels, item.id, item.sealed, &channel); err != nil {
			return fmt.Errorf("opening channel %s: %w", item.id, err)
		}
		if channel.ID != item.id || channel.Type == "" || strings.TrimSpace(channel.Name) == "" {
			return fmt.Errorf("channel %s has an invalid encrypted payload", item.id)
		}
		public, secret := splitChannelSettings(channel.Settings)
		publicJSON, err := json.Marshal(public)
		if err != nil {
			return err
		}
		secretSealed, err := sealJSON(v, tableChannelSecrets, item.id, secret)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channels_v10(id,name,type,enabled,public_settings_json,secret_sealed,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
			channel.ID, channel.Name, channel.Type, boolToInt(channel.Enabled), string(publicJSON), secretSealed, channel.CreatedAt.Unix(), channel.UpdatedAt.Unix()); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DROP TABLE channels; ALTER TABLE channels_v10 RENAME TO channels; CREATE INDEX idx_channels_enabled ON channels(enabled, type, id);`)
	return err
}

func splitChannelSettings(settings map[string]string) (map[string]string, map[string]string) {
	public, secret := make(map[string]string), make(map[string]string)
	for key, value := range settings {
		switch key {
		case "password", "refresh_token", "webhook", "secret", "app_secret", "access_token":
			secret[key] = value
		default:
			public[key] = value
		}
	}
	return public, secret
}

func migrateV10SyncTargets(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
ALTER TABLE sync_targets ADD COLUMN source_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_targets ADD COLUMN comment_type INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_targets ADD COLUMN comment_oid TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_targets ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_targets ADD COLUMN url TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_targets ADD COLUMN published_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_targets ADD COLUMN comment_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_targets ADD COLUMN closed INTEGER NOT NULL DEFAULT 0;

UPDATE sync_targets
SET source_id = COALESCE((SELECT 'bilibili:up:' || uid FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), source_id),
    comment_type = COALESCE((SELECT comment_type FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), comment_type),
    comment_oid = COALESCE((SELECT comment_oid FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), comment_oid),
    title = COALESCE((SELECT title FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), title),
    url = COALESCE((SELECT url FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), url),
    published_at = COALESCE((SELECT published_at FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), published_at),
    comment_count = COALESCE((SELECT comment_count FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), comment_count),
    closed = COALESCE((SELECT closed FROM comment_targets c WHERE 'bilibili:content:' || c.dynamic_id = sync_targets.content_id LIMIT 1), closed);
CREATE INDEX idx_sync_targets_source_pub ON sync_targets(source_id, published_at DESC, content_id);
`)
	return err
}

func migrateV10Outbox(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE outbox_v10 (
  id                 TEXT PRIMARY KEY,
  kind               TEXT NOT NULL CHECK(kind IN ('content','comment','ai','system')),
  platform           TEXT NOT NULL DEFAULT '',
  source_id          TEXT NOT NULL DEFAULT '',
  content_id         TEXT NOT NULL DEFAULT '',
  channel_id         TEXT NOT NULL,
  idempotency_key    TEXT NOT NULL,
  state              TEXT NOT NULL CHECK(state IN ('pending','blocked')),
  attempts           INTEGER NOT NULL DEFAULT 0,
  next_at            INTEGER NOT NULL,
  last_error         TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  title              TEXT NOT NULL DEFAULT '',
  summary            TEXT NOT NULL DEFAULT '',
  payload_json       TEXT NOT NULL,
  progress_json      TEXT NOT NULL DEFAULT '{}',
  origin_traceparent TEXT NOT NULL DEFAULT '',
  UNIQUE(channel_id, idempotency_key)
);`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,platform,source_id,content_id,channel_id,idempotency_key,state,attempts,next_at,last_error,created_at,payload_json,progress_json,origin_traceparent FROM outbox ORDER BY id`)
	if err != nil {
		return err
	}
	var pending []v9Outbox
	for rows.Next() {
		var row v9Outbox
		if err := rows.Scan(&row.ID, &row.Kind, &row.Platform, &row.SourceID, &row.ContentID, &row.ChannelID, &row.IdempotencyKey, &row.State, &row.Attempts, &row.NextAt, &row.LastError, &row.CreatedAt, &row.PayloadJSON, &row.ProgressJSON, &row.Traceparent); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range pending {
		delivery, err := v9PlatformDelivery(ctx, tx, row)
		if err != nil {
			return fmt.Errorf("decoding transitional outbox %s: %w", row.ID, err)
		}
		if err := insertV10Delivery(ctx, tx, delivery, row.Platform, row.SourceID, row.ContentID, row.IdempotencyKey); err != nil {
			return err
		}
	}
	legacy, err := tx.QueryContext(ctx, `SELECT id,kind,channel_id,state,attempts,next_at,last_error,created_at,payload_json FROM deliveries ORDER BY id`)
	if err != nil {
		return err
	}
	var deliveries []legacyDeliveryRow
	for legacy.Next() {
		var row legacyDeliveryRow
		if err := legacy.Scan(&row.ID, &row.Kind, &row.ChannelID, &row.State, &row.Attempts, &row.NextAt, &row.LastError, &row.CreatedAt, &row.PayloadJSON); err != nil {
			_ = legacy.Close()
			return err
		}
		deliveries = append(deliveries, row)
	}
	if err := legacy.Err(); err != nil {
		_ = legacy.Close()
		return err
	}
	if err := legacy.Close(); err != nil {
		return err
	}
	for _, row := range deliveries {
		var delivery model.Delivery
		if err := json.Unmarshal([]byte(row.PayloadJSON), &delivery); err != nil {
			return fmt.Errorf("decoding delivery %s: %w", row.ID, err)
		}
		delivery.ID, delivery.Kind, delivery.ChannelID = row.ID, model.DeliveryKind(row.Kind), row.ChannelID
		delivery.State, delivery.Attempts = model.DeliveryState(row.State), row.Attempts
		delivery.NextAt, delivery.LastError, delivery.CreatedAt = time.Unix(row.NextAt, 0), row.LastError, time.Unix(row.CreatedAt, 0)
		normalizeLegacyDelivery(&delivery)
		platform, sourceID, contentID, err := validateDeliverySnapshot(delivery)
		if err != nil {
			return fmt.Errorf("validating delivery %s: %w", row.ID, err)
		}
		if delivery.Kind == model.DeliveryKindAI && sourceID == "" && contentID != "" {
			if err := tx.QueryRowContext(ctx, `SELECT source_id FROM contents WHERE id=?`, contentID).Scan(&sourceID); err != nil {
				return fmt.Errorf("loading AI delivery %s source: %w", row.ID, err)
			}
			delivery.AI.SourceID = sourceID
		}
		if err := insertV10Delivery(ctx, tx, delivery, platform, sourceID, contentID, row.ID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
DROP TABLE outbox;
ALTER TABLE outbox_v10 RENAME TO outbox;
CREATE INDEX idx_outbox_due ON outbox(state, next_at, id);
CREATE INDEX idx_outbox_source ON outbox(source_id, created_at, id);
CREATE INDEX idx_outbox_content ON outbox(content_id, created_at, id);
CREATE INDEX idx_outbox_cursor ON outbox(created_at DESC, id DESC);
CREATE INDEX idx_outbox_channel ON outbox(channel_id, state, id);
`)
	return err
}

func normalizeLegacyDelivery(delivery *model.Delivery) {
	switch delivery.Kind {
	case model.DeliveryKindDynamic:
		if delivery.Dynamic.UID != "system" {
			if !strings.HasPrefix(delivery.Dynamic.UID, "bilibili:up:") {
				delivery.Dynamic.UID = model.SourceID(model.PlatformBilibili, delivery.Dynamic.UID)
			}
			if !strings.HasPrefix(delivery.Dynamic.ID, "bilibili:content:") {
				delivery.Dynamic.ID = model.ContentID(model.PlatformBilibili, delivery.Dynamic.ID)
			}
			delivery.Dynamic.Platform = model.PlatformBilibili
		}
	case model.DeliveryKindComment:
		if delivery.Comment == nil {
			return
		}
		if !strings.HasPrefix(delivery.Comment.UPUID, "bilibili:up:") {
			delivery.Comment.UPUID = model.SourceID(model.PlatformBilibili, delivery.Comment.UPUID)
		}
		if !strings.HasPrefix(delivery.Comment.ContentID, "bilibili:content:") {
			delivery.Comment.ContentID = model.ContentID(model.PlatformBilibili, delivery.Comment.ContentID)
		}
		delivery.Comment.Platform = model.PlatformBilibili
	case model.DeliveryKindAI:
		if delivery.AI != nil && !strings.HasPrefix(delivery.AI.DynamicID, "bilibili:content:") {
			delivery.AI.DynamicID = model.ContentID(model.PlatformBilibili, delivery.AI.DynamicID)
		}
	}
}

type v9Outbox struct {
	ID, Kind, Platform, SourceID, ContentID, ChannelID, IdempotencyKey string
	State, LastError, PayloadJSON, ProgressJSON, Traceparent           string
	Attempts                                                           int
	NextAt, CreatedAt                                                  int64
}

type legacyDeliveryRow struct {
	ID, Kind, ChannelID, State, LastError, PayloadJSON string
	Attempts                                           int
	NextAt, CreatedAt                                  int64
}

func v9PlatformDelivery(ctx context.Context, tx *sql.Tx, row v9Outbox) (model.Delivery, error) {
	delivery := model.Delivery{ID: row.ID, ChannelID: row.ChannelID, State: model.DeliveryState(row.State), Attempts: row.Attempts,
		NextAt: time.Unix(row.NextAt, 0), LastError: row.LastError, CreatedAt: time.Unix(row.CreatedAt, 0), OriginTraceparent: row.Traceparent}
	if delivery.State == "" {
		delivery.State = model.DeliveryPending
	}
	if row.ProgressJSON != "" {
		var progress model.DeliveryProgress
		if err := json.Unmarshal([]byte(row.ProgressJSON), &progress); err != nil {
			return model.Delivery{}, fmt.Errorf("decoding progress: %w", err)
		}
		delivery.Progress = &progress
	}
	switch row.Kind {
	case "content":
		var content model.Content
		if err := json.Unmarshal([]byte(row.PayloadJSON), &content); err != nil {
			return model.Delivery{}, err
		}
		var sourceName string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM sources WHERE id=?`, row.SourceID).Scan(&sourceName); err != nil {
			return model.Delivery{}, err
		}
		delivery.Kind = model.DeliveryKindDynamic
		delivery.Dynamic = model.Dynamic{ID: content.ID, Platform: content.Platform, SourceName: sourceName, UID: content.SourceID,
			UPName: content.AuthorName, Type: string(content.Type), PublishedAt: content.PublishedAt, Summary: content.Text,
			Description: content.Text, URL: content.URL, Title: content.Title}
		attachments, err := tx.QueryContext(ctx, `SELECT type,file_name,mime,size,width,height,local_path FROM attachments WHERE content_id=? ORDER BY id`, content.ID)
		if err != nil {
			return model.Delivery{}, err
		}
		for attachments.Next() {
			var kind, name, mime, localPath string
			var size int64
			var width, height int
			if err := attachments.Scan(&kind, &name, &mime, &size, &width, &height, &localPath); err != nil {
				_ = attachments.Close()
				return model.Delivery{}, err
			}
			if kind == string(model.AttachmentImage) && localPath != "" {
				delivery.Dynamic.Media = append(delivery.Dynamic.Media, model.DynamicMedia{Kind: model.DynamicMediaImage, Width: width, Height: height, LocalPath: localPath, ContentType: mime, Size: size})
			} else {
				delivery.Dynamic.Links = append(delivery.Dynamic.Links, model.DynamicLink{Text: name, URL: content.URL})
			}
		}
		if err := attachments.Err(); err != nil {
			_ = attachments.Close()
			return model.Delivery{}, err
		}
		if err := attachments.Close(); err != nil {
			return model.Delivery{}, err
		}
	case "comment_digest":
		var digest model.CommentDigest
		if err := json.Unmarshal([]byte(row.PayloadJSON), &digest); err != nil {
			return model.Delivery{}, err
		}
		var sourceName string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM sources WHERE id=?`, row.SourceID).Scan(&sourceName); err != nil {
			return model.Delivery{}, err
		}
		thread, seen, published := make([]model.CommentNode, 0), make(map[string]bool), time.Time{}
		for _, path := range digest.Paths {
			for _, node := range path.Nodes {
				if !seen[node.ID] {
					node.IsTrigger = node.ID == path.TriggerID
					thread, seen[node.ID] = append(thread, node), true
				}
				if node.Time.After(published) {
					published = node.Time
				}
			}
		}
		label, contentType := "UP主", "bilibili"
		if digest.Platform == model.PlatformZSXQ {
			label, contentType = "星球主", "zsxq"
		}
		note := model.CommentNotification{RPID: row.IdempotencyKey, Platform: digest.Platform, SourceName: sourceName,
			UPUID: digest.SourceID, UPName: label, ContentType: contentType, ContentID: digest.ContentID,
			ContentTitle: digest.Title, ContentURL: digest.ContentURL, PublishedAt: published, Incomplete: digest.Incomplete, Thread: thread}
		if len(digest.Triggers) == 1 {
			note.RPID = digest.Triggers[0].RPID
		}
		delivery.Kind, delivery.Comment = model.DeliveryKindComment, &note
	case "ai":
		var notification model.AINotification
		if err := json.Unmarshal([]byte(row.PayloadJSON), &notification); err != nil {
			return model.Delivery{}, err
		}
		notification.SourceID = row.SourceID
		delivery.Kind, delivery.AI = model.DeliveryKindAI, &notification
	default:
		return model.Delivery{}, fmt.Errorf("unsupported outbox kind %q", row.Kind)
	}
	return delivery, nil
}

func insertV10Delivery(ctx context.Context, tx *sql.Tx, delivery model.Delivery, platform, sourceID, contentID, idempotencyKey string) error {
	if _, _, _, err := validateDeliverySnapshot(delivery); err != nil {
		return fmt.Errorf("validating immutable snapshot %s: %w", delivery.ID, err)
	}
	payload, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	progress, err := json.Marshal(delivery.Progress)
	if err != nil {
		return err
	}
	kind, title, summary := "content", delivery.Dynamic.Title, delivery.Dynamic.Summary
	switch delivery.Kind {
	case model.DeliveryKindComment:
		kind, title = "comment", delivery.Comment.ContentTitle
		if len(delivery.Comment.Thread) != 0 {
			summary = delivery.Comment.Thread[len(delivery.Comment.Thread)-1].Message
		}
	case model.DeliveryKindAI:
		kind, title, summary = "ai", delivery.AI.Title, delivery.AI.Body
	default:
		if delivery.Dynamic.UID == "system" {
			kind = "system"
		}
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = delivery.ID
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO outbox_v10(id,kind,platform,source_id,content_id,channel_id,idempotency_key,state,attempts,next_at,last_error,created_at,title,summary,payload_json,progress_json,origin_traceparent) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		delivery.ID, kind, platform, sourceID, contentID, delivery.ChannelID, idempotencyKey, delivery.State, delivery.Attempts,
		delivery.NextAt.Unix(), delivery.LastError, delivery.CreatedAt.Unix(), title, summary, string(payload), string(progress), delivery.OriginTraceparent)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var existingChannel, existingKey string
		if err := tx.QueryRowContext(ctx, `SELECT channel_id,idempotency_key FROM outbox_v10 WHERE id=?`, delivery.ID).Scan(&existingChannel, &existingKey); err != nil {
			return err
		}
		if existingChannel != delivery.ChannelID || existingKey != idempotencyKey {
			return fmt.Errorf("conflicting outbox identity %s", delivery.ID)
		}
	}
	return nil
}

func validateDeliverySnapshot(delivery model.Delivery) (platform, sourceID, contentID string, err error) {
	if delivery.ID == "" || delivery.ChannelID == "" || (delivery.State != model.DeliveryPending && delivery.State != model.DeliveryBlocked) || delivery.NextAt.IsZero() || delivery.CreatedAt.IsZero() {
		return "", "", "", errors.New("missing required scheduling fields")
	}
	switch delivery.Kind {
	case "", model.DeliveryKindDynamic:
		if delivery.Dynamic.ID == "" {
			return "", "", "", errors.New("content snapshot is missing id")
		}
		platform, sourceID, contentID = string(delivery.Dynamic.Platform), delivery.Dynamic.UID, delivery.Dynamic.ID
		if delivery.Dynamic.Platform == "" && delivery.Dynamic.UID != "system" {
			platform = string(model.PlatformBilibili)
		}
	case model.DeliveryKindComment:
		if delivery.Comment == nil || delivery.Comment.RPID == "" {
			return "", "", "", errors.New("comment snapshot is missing payload")
		}
		platform, sourceID, contentID = string(delivery.Comment.Platform), delivery.Comment.UPUID, delivery.Comment.ContentID
		if platform == "" {
			platform = string(model.PlatformBilibili)
		}
	case model.DeliveryKindAI:
		if delivery.AI == nil || delivery.AI.JobID == "" {
			return "", "", "", errors.New("AI snapshot is missing payload")
		}
		platform, sourceID, contentID = string(model.PlatformBilibili), delivery.AI.SourceID, delivery.AI.DynamicID
	default:
		return "", "", "", fmt.Errorf("unsupported delivery kind %q", delivery.Kind)
	}
	return platform, sourceID, contentID, nil
}

func migrateV10AIJobs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
ALTER TABLE ai_jobs RENAME COLUMN source_dynamic_id TO source_content_id;
ALTER TABLE ai_jobs ADD COLUMN source_snapshot_json TEXT NOT NULL DEFAULT '{}';
UPDATE ai_jobs
SET source_snapshot_json = COALESCE((SELECT raw_json FROM contents WHERE contents.id = ai_jobs.source_content_id), '{}')
WHERE source_content_id != '';
DROP INDEX IF EXISTS idx_ai_jobs_dynamic;
CREATE INDEX idx_ai_jobs_content ON ai_jobs(source_content_id, created_at);
`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT ai_jobs.id, ai_jobs.source_content_id, ai_jobs.source_snapshot_json, COALESCE(contents.source_id, '')
FROM ai_jobs
LEFT JOIN contents ON contents.id = ai_jobs.source_content_id
WHERE ai_jobs.source_content_id != ''`)
	if err != nil {
		return err
	}
	type sourceSnapshot struct {
		id, contentID, snapshot, sourceID string
	}
	var snapshots []sourceSnapshot
	for rows.Next() {
		var item sourceSnapshot
		if err := rows.Scan(&item.id, &item.contentID, &item.snapshot, &item.sourceID); err != nil {
			_ = rows.Close()
			return err
		}
		snapshots = append(snapshots, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range snapshots {
		var value map[string]json.RawMessage
		if err := json.Unmarshal([]byte(item.snapshot), &value); err != nil {
			return fmt.Errorf("AI job %s source snapshot: %w", item.id, err)
		}
		if item.contentID == "" || item.sourceID == "" {
			return fmt.Errorf("AI job %s has an invalid content reference", item.id)
		}
		readString := func(keys ...string) string {
			for _, key := range keys {
				var result string
				if raw := value[key]; len(raw) != 0 && json.Unmarshal(raw, &result) == nil && result != "" {
					return result
				}
			}
			return ""
		}
		normalized := model.AIContentSnapshot{ContentID: item.contentID, SourceID: item.sourceID, BVID: readString("bvid"),
			Author: readString("up_name", "author_name"), Title: readString("title"), URL: readString("target_url", "url")}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_jobs SET source_snapshot_json=? WHERE id=?`, string(raw), item.id); err != nil {
			return err
		}
	}
	return nil
}

var _ = goose.GoMigrationContext(nil)

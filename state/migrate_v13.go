package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

type v13OutboxRow struct {
	ID, Kind, Platform, SourceID, ContentID, ChannelID, IdempotencyKey string
	State, LastError, PayloadJSON, ProgressJSON, Traceparent           string
	Attempts                                                           int
	NextAt, CreatedAt                                                  int64
}

func migrateV13(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE outbox_v13 (
  id                 TEXT PRIMARY KEY,
  kind               TEXT NOT NULL CHECK(kind IN ('content','comment','ai','system')),
  platform           TEXT NOT NULL DEFAULT '',
  source_id          TEXT NOT NULL DEFAULT '',
  content_id         TEXT NOT NULL DEFAULT '',
  channel_id         TEXT NOT NULL,
  idempotency_key    TEXT NOT NULL,
  state              TEXT NOT NULL CHECK(state IN ('pending','blocked','suspended')),
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
	var pending []v13OutboxRow
	for rows.Next() {
		var row v13OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Platform, &row.SourceID, &row.ContentID, &row.ChannelID, &row.IdempotencyKey,
			&row.State, &row.Attempts, &row.NextAt, &row.LastError, &row.CreatedAt, &row.PayloadJSON, &row.ProgressJSON, &row.Traceparent); err != nil {
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
		delivery, conversionErr := v13Delivery(row)
		if conversionErr != nil {
			delivery = blockedConversionDelivery(row, conversionErr)
		}
		var enabled int
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM channels WHERE id=?`, row.ChannelID).Scan(&enabled); err != nil {
			return fmt.Errorf("loading outbox channel %s: %w", row.ChannelID, err)
		}
		if enabled == 0 && delivery.State == model.DeliveryPending {
			delivery.State = model.DeliverySuspended
		}
		platform, sourceID, contentID, err := validateDeliverySnapshot(delivery)
		if err != nil {
			return fmt.Errorf("validating converted delivery %s: %w", row.ID, err)
		}
		payload, err := json.Marshal(delivery)
		if err != nil {
			return err
		}
		progress, err := json.Marshal(delivery.Progress)
		if err != nil {
			return err
		}
		kind, title, summary := deliveryMetadata(delivery)
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox_v13(id,kind,platform,source_id,content_id,channel_id,idempotency_key,state,attempts,next_at,last_error,created_at,title,summary,payload_json,progress_json,origin_traceparent) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			delivery.ID, kind, platform, sourceID, contentID, delivery.ChannelID, row.IdempotencyKey, delivery.State, delivery.Attempts,
			delivery.NextAt.Unix(), delivery.LastError, delivery.CreatedAt.Unix(), title, summary, string(payload), string(progress), delivery.OriginTraceparent); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
DROP TABLE outbox;
ALTER TABLE outbox_v13 RENAME TO outbox;
CREATE INDEX idx_outbox_due ON outbox(state, next_at, id);
CREATE INDEX idx_outbox_source ON outbox(source_id, created_at, id);
CREATE INDEX idx_outbox_content ON outbox(content_id, created_at, id);
CREATE INDEX idx_outbox_cursor ON outbox(created_at DESC, id DESC);
CREATE INDEX idx_outbox_channel ON outbox(channel_id, state, id);`)
	return err
}

func v13Delivery(row v13OutboxRow) (model.Delivery, error) {
	var delivery model.Delivery
	if err := json.Unmarshal([]byte(row.PayloadJSON), &delivery); err == nil {
		applyV13Schedule(&delivery, row)
		if _, _, _, err := validateDeliverySnapshot(delivery); err == nil {
			return delivery, nil
		}
	}
	var legacy legacyDeliveryPayload
	if err := json.Unmarshal([]byte(row.PayloadJSON), &legacy); err != nil {
		return model.Delivery{}, err
	}
	delivery = normalizeLegacyDelivery(legacy)
	applyV13Schedule(&delivery, row)
	if delivery.Kind == "" {
		return model.Delivery{}, fmt.Errorf("unsupported legacy delivery kind %q", legacy.Kind)
	}
	return delivery, nil
}

func applyV13Schedule(delivery *model.Delivery, row v13OutboxRow) {
	delivery.ID, delivery.ChannelID = row.ID, row.ChannelID
	delivery.State, delivery.Attempts = model.DeliveryState(row.State), row.Attempts
	delivery.NextAt, delivery.LastError, delivery.CreatedAt = time.Unix(row.NextAt, 0), row.LastError, time.Unix(row.CreatedAt, 0)
	delivery.OriginTraceparent = row.Traceparent
	if row.ProgressJSON != "" {
		_ = json.Unmarshal([]byte(row.ProgressJSON), &delivery.Progress)
	}
}

func blockedConversionDelivery(row v13OutboxRow, conversionErr error) model.Delivery {
	createdAt := time.Unix(row.CreatedAt, 0)
	message := "legacy outbox payload could not be converted: " + conversionErr.Error()
	alert := model.SystemAlert{ID: "migration:" + row.ID, Title: "Outbox payload conversion blocked", Body: message, CreatedAt: createdAt}
	return model.Delivery{ID: row.ID, Kind: model.DeliveryKindSystem, System: &alert, ChannelID: row.ChannelID,
		State: model.DeliveryBlocked, Attempts: row.Attempts, NextAt: time.Unix(row.NextAt, 0), LastError: strings.TrimSpace(message),
		CreatedAt: createdAt, OriginTraceparent: row.Traceparent}
}

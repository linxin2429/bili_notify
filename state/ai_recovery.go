package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type automaticAIIntent struct {
	Dynamic     model.Dynamic
	Channels    []string
	Traceparent string
	Tracestate  string
}

// A savepoint isolates optional AI setup from the content/outbox transaction.
// Failure leaves a durable intent, never a half-created transcription pipeline.
func (s *Store) createAutomaticAIOrDeferTx(tx *gorm.DB, dynamic model.Dynamic, sourceID string, channels []string) error {
	if dynamic.Type != "DYNAMIC_TYPE_AV" || dynamic.BVID == "" {
		return nil
	}
	err := tx.Transaction(func(nested *gorm.DB) error {
		_, err := s.createAutomaticAIJobsTx(nested, dynamic, sourceID, channels)
		return err
	})
	if err == nil {
		return nil
	}
	payload, marshalErr := json.Marshal(automaticAIIntent{dynamic, channels, originTraceparent(tx.Statement.Context), originTracestate(tx.Statement.Context)})
	if marshalErr != nil {
		return marshalErr
	}
	item := CollectionItem{SourceID: sourceID, ID: dynamic.ID, Kind: "ai", Payload: payload,
		Attempts: 1, NextAt: time.Now().Add(CollectionRetryDelay(1)).Unix(), LastError: err.Error(), CreatedAt: time.Now().UnixNano()}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}

// RetryAutomaticAI resumes durable scheduling intents independently of Bilibili
// authentication and content discovery. Individual failures keep their backoff.
func (s *Store) RetryAutomaticAI(at time.Time) error {
	items, err := s.DueCollectionItems("", "ai", at, 20)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		var intent automaticAIIntent
		err := json.Unmarshal(item.Payload, &intent)
		if err == nil {
			ctx := propagation.TraceContext{}.Extract(s.db.Statement.Context, propagation.MapCarrier{"traceparent": intent.Traceparent, "tracestate": intent.Tracestate})
			err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var current CollectionItem
				if err := tx.Where("source_id = ? AND id = ? AND kind = ?", item.SourceID, item.ID, item.Kind).Take(&current).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return nil
					}
					return err
				}
				if _, err := s.createAutomaticAIJobsTx(tx, intent.Dynamic, item.SourceID, intent.Channels); err != nil {
					return err
				}
				return tx.Where("source_id = ? AND id = ? AND kind = ?", item.SourceID, item.ID, item.Kind).Delete(&CollectionItem{}).Error
			})
		}
		if err != nil {
			if updateErr := s.FailCollectionItem(item, at, err); updateErr != nil {
				return fmt.Errorf("deferring automatic AI: %w", updateErr)
			}
			failures = append(failures, fmt.Errorf("dynamic %s automatic AI scheduling: %w", item.ID, err))
		}
	}
	return errors.Join(failures...)
}

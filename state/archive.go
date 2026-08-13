package state

import (
	"errors"
	"fmt"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ArchiveItem is one platform-neutral content write. Snapshot is sealed into
// the outbox in the same transaction; AutomaticAI is nil when the adapter does
// not consider the content eligible for automatic processing.
type ArchiveItem struct {
	Content     model.Content
	Attachments []model.Attachment
	Snapshot    model.ContentSnapshot
	Notify      bool
	AutomaticAI *model.AIContentSnapshot
}

type SourceArchive struct {
	SourceID                  string
	Items                     []ArchiveItem
	CompleteBaseline          bool
	CompleteExclusiveBaseline bool
}

type FeedArchive struct {
	AccountUID        string
	UpdateBaseline    string
	Items             []ArchiveItem
	FailedExternalIDs []string
}

// ArchiveSourceBatch archives a platform adapter's source-poll result and
// advances Bilibili's two source baselines atomically when requested.
func (s *Store) ArchiveSourceBatch(batch SourceArchive) (int, error) {
	if batch.SourceID == "" {
		return 0, errors.New("source id is required")
	}
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		count, err := s.archiveItemsTx(tx, batch.Items)
		if err != nil {
			return err
		}
		created = count
		if !batch.CompleteBaseline && !batch.CompleteExclusiveBaseline {
			return nil
		}
		updates := map[string]any{"exclusive_baseline_ready": 1}
		if batch.CompleteBaseline {
			updates["baseline_state"] = model.BaselineComplete
		}
		result := tx.Model(&sourceRow{}).Where("id = ?", batch.SourceID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	return created, err
}

// ArchiveFeedBatch archives an aggregate-feed page set and advances its
// adapter-specific routing watermark in the same transaction.
func (s *Store) ArchiveFeedBatch(batch FeedArchive) (int, error) {
	if batch.AccountUID == "" || batch.UpdateBaseline == "" {
		return 0, errors.New("account uid and update baseline are required")
	}
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		count, err := s.archiveItemsTx(tx, batch.Items)
		if err != nil {
			return err
		}
		created = count
		now := time.Now()
		feed := biliFeedStateRow{AccountUID: batch.AccountUID, UpdateBaseline: batch.UpdateBaseline, Initialized: 1, UpdatedAt: now.Unix()}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_uid"}},
			DoUpdates: clause.AssignmentColumns([]string{"update_baseline", "initialized", "updated_at"}),
		}).Create(&feed).Error; err != nil {
			return err
		}
		if len(batch.FailedExternalIDs) == 0 {
			return nil
		}
		return tx.Model(&upFollowRelationRow{}).
			Where("account_uid = ? AND up_uid IN ?", batch.AccountUID, batch.FailedExternalIDs).
			Update("space_synced", 0).Error
	})
	if err != nil {
		return 0, fmt.Errorf("archiving feed batch: %w", err)
	}
	return created, nil
}

func (s *Store) archiveItemsTx(tx *gorm.DB, items []ArchiveItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	autoAI, err := automaticAIEnabledTx(tx)
	if err != nil {
		return 0, err
	}
	channelIDs, err := enabledChannelIDsTx(tx)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range items {
		if item.Content.ID == "" {
			continue
		}
		var existing int64
		if err := tx.Model(&contentRow{}).Where("id = ?", item.Content.ID).Count(&existing).Error; err != nil {
			return 0, err
		}
		if err := archiveContentTx(tx, item.Content, item.Attachments); err != nil {
			return 0, fmt.Errorf("archiving content %s: %w", item.Content.ID, err)
		}
		if existing != 0 {
			continue
		}
		if item.Notify {
			now := time.Now()
			for _, channelID := range channelIDs {
				delivery := model.Delivery{ID: stableHash("content", item.Content.ID, channelID), Kind: model.DeliveryKindContent,
					Content: &item.Snapshot, ChannelID: channelID, State: model.DeliveryPending, NextAt: now, CreatedAt: now,
					OriginTraceparent: originTraceparent(tx.Statement.Context)}
				if err := putDeliveryTx(tx, delivery); err != nil {
					return 0, err
				}
			}
		}
		if item.Notify && autoAI && item.AutomaticAI != nil {
			if _, err := s.createAutomaticAIJobsTx(tx, *item.AutomaticAI, channelIDs); err != nil {
				return 0, fmt.Errorf("creating automatic AI pipeline for content %s: %w", item.Content.ID, err)
			}
		}
		if item.Notify {
			created++
		}
	}
	return created, nil
}

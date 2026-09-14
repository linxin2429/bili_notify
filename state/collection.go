package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollectionItem is a durable discovery or deferred AI scheduling intent.
// Discoveries are not seen markers: they must survive advancing the scan cursor.
type CollectionItem struct {
	SourceID     string `gorm:"primaryKey"`
	ID           string `gorm:"primaryKey"`
	Kind         string `gorm:"primaryKey"`
	Payload      []byte
	BaselineMode DynamicBaselineMode
	Attempts     int
	NextAt       int64
	LastError    string
	CreatedAt    int64
}

// CollectionScan checkpoints a bounded space scan and its request backoff.
type CollectionScan struct {
	SourceID       string `gorm:"primaryKey"`
	Offset         string
	Active         bool
	BaselineMode   DynamicBaselineMode
	SeenThrough    int64
	PendingThrough int64
	Attempts       int
	NextAt         int64
	LastError      string
}

// CollectionScan returns the saved scan for a UP, or a fresh scan if absent.
// Active scans retain their original seen/pending boundaries. Inactive scans
// snapshot the current seen row ID and discovery time for CollectionKnown.
func (s *Store) CollectionScan(uid string) (CollectionScan, error) {
	row := CollectionScan{SourceID: model.SourceID(model.PlatformBilibili, uid)}
	err := s.db.Where("source_id = ?", row.SourceID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	if err == nil && !row.Active {
		err = s.db.Model(&seenItemRow{}).Select("COALESCE(MAX(rowid), 0)").Scan(&row.SeenThrough).Error
		row.PendingThrough = time.Now().UnixNano()
	}
	if err != nil {
		return row, fmt.Errorf("reading collection scan for %s: %w", uid, err)
	}
	return row, nil
}

// StageCollectionPage commits raw items before allowing their cursor to advance.
// Existing retries keep their baseline semantics, attempts and retry deadline.
func (s *Store) StageCollectionPage(items []CollectionItem, scan *CollectionScan) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			if item.SourceID == "" || item.ID == "" || item.Kind == "" || !json.Valid(item.Payload) {
				return errors.New("collection item requires source, id, kind and JSON payload")
			}
			item.CreatedAt = time.Now().UnixNano()
			if item.NextAt == 0 {
				item.NextAt = time.Now().Unix()
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "source_id"}, {Name: "id"}, {Name: "kind"}},
				DoUpdates: clause.AssignmentColumns([]string{"payload"}),
			}).Create(&item).Error; err != nil {
				return err
			}
		}
		if scan != nil {
			return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(scan).Error
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("staging collection page: %w", err)
	}
	return nil
}

// CollectionKnown reports whether an item predates the scan's seen or pending
// snapshot. Discoveries and commits made during that scan cannot stop it early.
func (s *Store) CollectionKnown(uid, id string, scan CollectionScan) (bool, error) {
	var seen int64
	err := s.db.Model(&seenItemRow{}).Where("source_id = ? AND entity_id = ? AND entity_type = ? AND rowid <= ?", model.SourceID(model.PlatformBilibili, uid), model.ContentID(model.PlatformBilibili, id), "content", scan.SeenThrough).Count(&seen).Error
	if err != nil {
		return false, fmt.Errorf("checking collection seen boundary: %w", err)
	}
	if seen > 0 {
		return true, nil
	}
	var count int64
	err = s.db.Model(&CollectionItem{}).Where("source_id = ? AND id = ? AND kind = ? AND created_at <= ?", model.SourceID(model.PlatformBilibili, uid), id, "dynamic", scan.PendingThrough).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("checking collection pending boundary: %w", err)
	}
	return count > 0, nil
}

type collectionFeedGap struct {
	AccountUID string `gorm:"primaryKey"`
	ID         string `gorm:"primaryKey"`
	Payload    []byte
	CreatedAt  int64
}

// StageFeedPage persists even unassignable cards before the account cursor moves.
// Gaps trigger space reconciliation, while valid cards remain independently usable.
// Only the latest 100 distinct gap payloads are retained as diagnostic samples;
// recovery itself uses the independent space checkpoints, not these samples.
func (s *Store) StageFeedPage(accountUID string, items []CollectionItem, gaps []json.RawMessage) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		store := *s
		store.db = tx
		if err := store.StageCollectionPage(items, nil); err != nil {
			return err
		}
		for _, raw := range gaps {
			row := collectionFeedGap{AccountUID: accountUID, ID: stableHash(string(raw)), Payload: raw, CreatedAt: time.Now().Unix()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`DELETE FROM collection_feed_gaps WHERE rowid IN (SELECT rowid FROM collection_feed_gaps ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET 100)`).Error
	})
	if err != nil {
		return fmt.Errorf("staging aggregate feed page: %w", err)
	}
	return nil
}

// DueCollectionItems returns due work of one kind, oldest retry deadline first.
// An empty uid selects all UPs; limit is clamped to [1, 100]. Reading does not
// claim or delete rows, and callers must preserve each item's baseline mode.
func (s *Store) DueCollectionItems(uid, kind string, at time.Time, limit int) ([]CollectionItem, error) {
	query := s.db.Where("kind = ? AND next_at <= ?", kind, at.Unix())
	if uid != "" {
		query = query.Where("source_id = ?", model.SourceID(model.PlatformBilibili, uid))
	}
	var rows []CollectionItem
	err := query.Order("next_at, created_at, id").Limit(max(1, min(limit, 100))).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("querying due collection items: %w", err)
	}
	return rows, nil
}

// CompleteCollectionItem deletes exactly the identified source/id/kind tuple.
// Missing items are ignored. Use RecordCollectedDynamic when a content commit
// and removal of its pending discovery must succeed atomically.
func (s *Store) CompleteCollectionItem(item CollectionItem) error {
	err := s.db.Where("source_id = ? AND id = ? AND kind = ?", item.SourceID, item.ID, item.Kind).Delete(&CollectionItem{}).Error
	if err != nil {
		return fmt.Errorf("completing collection item: %w", err)
	}
	return nil
}

// CollectionRetryDelay saturates before shifting, including corrupted counters.
func CollectionRetryDelay(attempts int) time.Duration {
	return min(time.Minute<<(min(max(attempts, 1), 7)-1), time.Hour)
}

// FailCollectionItem advances the supplied item's attempt count and retry time,
// preserving its payload and baseline mode. cause must be non-nil. A deleted
// item stays deleted, so a stale attempt cannot recreate a removed source.
func (s *Store) FailCollectionItem(item CollectionItem, at time.Time, cause error) error {
	attempts := min(max(item.Attempts, 0), 1000) + 1
	err := s.db.Model(&CollectionItem{}).Where("source_id = ? AND id = ? AND kind = ?", item.SourceID, item.ID, item.Kind).
		Updates(map[string]any{"attempts": attempts, "next_at": at.Add(CollectionRetryDelay(attempts)).Unix(), "last_error": cause.Error()}).Error
	if err != nil {
		return fmt.Errorf("deferring collection item: %w", err)
	}
	return nil
}

// CollectionPendingError reports the oldest pending dynamic even during backoff.
// It returns nil only when no dynamic remains; database errors are propagated.
func (s *Store) CollectionPendingError(uid string) error {
	var row CollectionItem
	err := s.db.Where("source_id = ? AND kind = ?", model.SourceID(model.PlatformBilibili, uid), "dynamic").Order("created_at, id").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading pending collection: %w", err)
	}
	return fmt.Errorf("dynamic %s pending retry: %s", row.ID, row.LastError)
}

// RecordCollectedDynamic atomically commits one item and removes its retry.
func (s *Store) RecordCollectedDynamic(item CollectionItem, dynamic model.Dynamic) (int, error) {
	if item.Kind != "dynamic" || item.ID != dynamic.ID || item.SourceID != model.SourceID(model.PlatformBilibili, dynamic.UID) {
		return 0, errors.New("collected dynamic identity does not match retry item")
	}
	var created int
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current CollectionItem
		if err := tx.Where("source_id = ? AND id = ? AND kind = ?", item.SourceID, item.ID, item.Kind).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		store := *s
		store.db = tx
		var err error
		created, err = store.RecordDynamics(dynamic.UID, []model.Dynamic{dynamic}, nil, current.BaselineMode)
		if err != nil {
			return err
		}
		return store.CompleteCollectionItem(item)
	})
	if err != nil {
		return 0, fmt.Errorf("recording collected dynamic %s: %w", item.ID, err)
	}
	return created, nil
}

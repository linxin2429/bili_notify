package state

import (
	"errors"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
)

func recordDynamicsForTest(store *Store, uid string, dynamics []model.Dynamic, _ []string, baselineMode DynamicBaselineMode) (int, error) {
	sourceID := model.SourceID(model.PlatformBilibili, uid)
	if _, err := store.Source(sourceID); errors.Is(err, ErrNotFound) {
		if err := store.PutUP(model.UP{UID: uid, Enabled: true}); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	items := make([]ArchiveItem, 0, len(dynamics))
	for _, dynamic := range dynamics {
		baseline := baselineMode.includes(dynamic)
		adapted := bilibili.AdaptDynamic(dynamic, baseline, time.Now())
		items = append(items, ArchiveItem{Content: adapted.Content, Attachments: adapted.Attachments, Snapshot: adapted.Snapshot,
			Notify: !baseline, AutomaticAI: adapted.AutomaticAI})
	}
	return store.ArchiveSourceBatch(SourceArchive{SourceID: sourceID, Items: items,
		CompleteBaseline: baselineMode == DynamicBaselineAll, CompleteExclusiveBaseline: baselineMode != DynamicBaselineNone})
}

func recordFeedDynamicsForTest(store *Store, accountUID, baseline string, dynamics []model.Dynamic, _ []string, failedUIDs []string) (int, error) {
	items := make([]ArchiveItem, 0, len(dynamics))
	for _, dynamic := range dynamics {
		sourceID := model.SourceID(model.PlatformBilibili, dynamic.UID)
		if _, err := store.Source(sourceID); errors.Is(err, ErrNotFound) {
			if err := store.PutUP(model.UP{UID: dynamic.UID, Enabled: true}); err != nil {
				return 0, err
			}
		} else if err != nil {
			return 0, err
		}
		adapted := bilibili.AdaptDynamic(dynamic, false, time.Now())
		items = append(items, ArchiveItem{Content: adapted.Content, Attachments: adapted.Attachments, Snapshot: adapted.Snapshot,
			Notify: true, AutomaticAI: adapted.AutomaticAI})
	}
	return store.ArchiveFeedBatch(FeedArchive{AccountUID: accountUID, UpdateBaseline: baseline, Items: items, FailedExternalIDs: failedUIDs})
}

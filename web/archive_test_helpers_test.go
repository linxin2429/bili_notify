package web

import (
	"errors"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
)

func recordDynamicsForWebTest(store *state.Store, uid string, dynamics []model.Dynamic, _ []string, baselineMode state.DynamicBaselineMode) (int, error) {
	sourceID := model.SourceID(model.PlatformBilibili, uid)
	if _, err := store.Source(sourceID); errors.Is(err, state.ErrNotFound) {
		if err := store.PutUP(model.UP{UID: uid, Enabled: true}); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	items := make([]state.ArchiveItem, 0, len(dynamics))
	for _, dynamic := range dynamics {
		baseline := baselineMode == state.DynamicBaselineAll || baselineMode == state.DynamicBaselineExclusive && dynamic.Exclusive
		adapted := bilibili.AdaptDynamic(dynamic, baseline, time.Now())
		items = append(items, state.ArchiveItem{Content: adapted.Content, Attachments: adapted.Attachments, Snapshot: adapted.Snapshot,
			Notify: !baseline, AutomaticAI: adapted.AutomaticAI})
	}
	return store.ArchiveSourceBatch(state.SourceArchive{SourceID: sourceID, Items: items,
		CompleteBaseline: baselineMode == state.DynamicBaselineAll, CompleteExclusiveBaseline: baselineMode != state.DynamicBaselineNone})
}

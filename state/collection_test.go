package state

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionRetryDelaySaturates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{name: "negative", attempt: -1, want: time.Minute},
		{name: "minimum", attempt: math.MinInt, want: time.Minute},
		{name: "zero", want: time.Minute},
		{name: "first", attempt: 1, want: time.Minute},
		{name: "second", attempt: 2, want: 2 * time.Minute},
		{name: "cap", attempt: 7, want: time.Hour},
		{name: "overflow", attempt: math.MaxInt, want: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { t.Parallel(); assert.Equal(t, tt.want, CollectionRetryDelay(tt.attempt)) })
	}
}

func TestCollectionPageTransactionAndDeletion(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 10)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	sourceID := model.SourceID(model.PlatformBilibili, "42")
	item := CollectionItem{SourceID: sourceID, ID: "one", Kind: "dynamic", Payload: []byte(`{"id_str":"one"}`), BaselineMode: DynamicBaselineAll}
	scan := CollectionScan{SourceID: sourceID, Offset: "next", Active: true}
	invalid := item
	invalid.ID = "two"
	invalid.Payload = []byte("invalid")
	err := store.StageCollectionPage([]CollectionItem{item, invalid}, &scan)
	require.ErrorContains(t, err, "JSON payload")
	rows, err := store.DueCollectionItems("42", "dynamic", time.Now(), 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
	gotScan, err := store.CollectionScan("42")
	require.NoError(t, err)
	assert.Empty(t, gotScan.Offset)
	require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, &scan))
	err = store.FailCollectionItem(item, time.Now(), errors.New("unavailable"))
	require.NoError(t, err)
	item.BaselineMode = DynamicBaselineNone
	item.Payload = []byte(`{"id_str":"one","type":"fixed"}`)
	require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
	rows, err = store.DueCollectionItems("42", "dynamic", time.Now(), 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = store.DueCollectionItems("42", "dynamic", time.Now().Add(2*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, DynamicBaselineAll, rows[0].BaselineMode)
	assert.Equal(t, 1, rows[0].Attempts)
	assert.Equal(t, item.Payload, rows[0].Payload)
	require.NoError(t, store.DeleteUP("42"))
	_, err = store.RecordCollectedDynamic(item, model.Dynamic{ID: "one", UID: "42", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.UP("42")
	require.ErrorIs(t, err, ErrNotFound, "a stale retry cannot resurrect a deleted source")
	rows, err = store.DueCollectionItems("42", "dynamic", time.Now().Add(2*time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
	var count int64
	require.NoError(t, store.db.Model(&CollectionScan{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestAutomaticAIFailureDoesNotRollbackCollectedContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		feed bool
	}{{name: "space"}, {name: "aggregate feed", feed: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 30)
			configureAutomaticAI(t, store, true)
			putEnabledTestChannel(t, store)
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
			// Simulate persisted configuration damage that normal admin validation prevents.
			require.NoError(t, store.db.Model(&aiProfileRow{}).Where("kind = ?", model.AIProfileText).Update("is_default", false).Error)
			dynamic := model.Dynamic{ID: "video", UID: "42", BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV", PublishedAt: time.Now()}
			var created int
			var err error
			if tt.feed {
				created, err = store.RecordFeedDynamics("100", "new", []model.Dynamic{dynamic}, nil, nil)
			} else {
				created, err = store.RecordDynamics("42", []model.Dynamic{dynamic}, nil, DynamicBaselineNone)
			}
			require.NoError(t, err)
			assert.Equal(t, 1, created)
			seen, err := store.Seen("42", "video")
			require.NoError(t, err)
			assert.True(t, seen)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, 1)
			jobs, err := store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "video"), false)
			require.NoError(t, err)
			assert.Empty(t, jobs)
			rows, err := store.DueCollectionItems("42", "ai", time.Now().Add(2*time.Hour), 100)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Contains(t, rows[0].LastError, "default summary profile")
			require.ErrorContains(t, store.RetryAutomaticAI(time.Now().Add(2*time.Hour)), "default summary profile")
			rows, err = store.DueCollectionItems("42", "ai", time.Now().Add(4*time.Hour), 100)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, 2, rows[0].Attempts)
			require.NoError(t, store.db.Model(&aiProfileRow{}).Where("kind = ?", model.AIProfileText).Update("is_default", true).Error)
			require.NoError(t, store.RetryAutomaticAI(time.Now().Add(4*time.Hour)))
			jobs, err = store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "video"), false)
			require.NoError(t, err)
			require.Len(t, jobs, 2)
			require.NoError(t, store.RetryAutomaticAI(time.Now().Add(5*time.Hour)))
			jobs, err = store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "video"), false)
			require.NoError(t, err)
			assert.Len(t, jobs, 2)
			rows, err = store.DueCollectionItems("42", "ai", time.Now().Add(5*time.Hour), 100)
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
}

func TestAutomaticAIHalfCreatedPipelineRollsBackToIntent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 31)
	configureAutomaticAI(t, store, true)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	require.NoError(t, store.db.Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON ai_jobs WHEN NEW.kind = 'summary' BEGIN SELECT RAISE(ABORT, 'test summary failure'); END`).Error)
	dynamic := model.Dynamic{ID: "video", UID: "42", BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV", PublishedAt: time.Now()}
	_, err := store.RecordDynamics("42", []model.Dynamic{dynamic}, nil, DynamicBaselineNone)
	require.NoError(t, err)
	jobs, err := store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "video"), false)
	require.NoError(t, err)
	assert.Empty(t, jobs, "transcription must roll back when creating summary fails")
	rows, err := store.DueCollectionItems("42", "ai", time.Now().Add(2*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, store.db.Exec("DROP TRIGGER fail_summary").Error)
	require.NoError(t, store.RetryAutomaticAI(time.Now().Add(2*time.Hour)))
	jobs, err = store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "video"), false)
	require.NoError(t, err)
	assert.Len(t, jobs, 2)
}

func TestDueRetryIsNotStarvedByNewDiscoveries(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 32)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	item := CollectionItem{SourceID: model.SourceID(model.PlatformBilibili, "42"), ID: "old", Kind: "dynamic", Payload: []byte(`{}`)}
	require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
	require.NoError(t, store.FailCollectionItem(item, time.Now().Add(-time.Hour), errors.New("retry")))
	item.ID = "new"
	require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
	rows, err := store.DueCollectionItems("42", "dynamic", time.Now(), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "old", rows[0].ID)
}

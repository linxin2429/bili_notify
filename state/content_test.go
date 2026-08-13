package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordDynamicsUsesUnifiedArchiveAndTransactionalChannels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		baseline       DynamicBaselineMode
		enabledChannel bool
		wantDeliveries int
	}{
		{name: "baseline without channel", baseline: DynamicBaselineAll},
		{name: "new content without channel", baseline: DynamicBaselineNone},
		{name: "new content with channel", baseline: DynamicBaselineNone, enabledChannel: true, wantDeliveries: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newContentStore(t)
			if tt.enabledChannel {
				_, err := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com"}})
				require.NoError(t, err)
			}
			now := time.Now()
			created, err := recordDynamicsForTest(store, "42", []model.Dynamic{{
				ID: "dynamic", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD", Summary: "body", PublishedAt: now,
			}}, []string{"ignored-by-transaction"}, tt.baseline)
			require.NoError(t, err)
			if tt.baseline == DynamicBaselineNone {
				assert.Equal(t, 1, created)
			} else {
				assert.Zero(t, created)
			}
			content, _, err := store.Content("bilibili:content:dynamic")
			require.NoError(t, err)
			assert.Equal(t, "bilibili:up:42", content.SourceID)
			assert.Equal(t, "body", content.Text)
			assert.Equal(t, tt.baseline != DynamicBaselineNone, content.Baseline)
			seen, err := store.Seen("42", "dynamic")
			require.NoError(t, err)
			assert.True(t, seen)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, tt.wantDeliveries)
		})
	}
}

func TestQueryContentsUsesUnifiedIndexedFields(t *testing.T) {
	t.Parallel()
	store := newContentStore(t)
	sources := []model.Source{
		{ID: "bilibili:up:1", Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "1", Name: "One", Enabled: true},
		{ID: "zsxq:planet:2", Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "2", Name: "Two", Enabled: true},
	}
	for _, source := range sources {
		require.NoError(t, store.PutSource(source))
	}
	base := time.Unix(1_700_000_000, 0)
	contents := []model.Content{
		{ID: "bilibili:content:1", Platform: model.PlatformBilibili, SourceID: sources[0].ID, ExternalID: "1", UpstreamType: "word", Type: model.ContentDynamic, Title: "Alpha", Text: "first", PublishedAt: base, FirstSeenAt: base, LastSyncedAt: base},
		{ID: "bilibili:content:2", Platform: model.PlatformBilibili, SourceID: sources[0].ID, ExternalID: "2", UpstreamType: "word", Type: model.ContentDynamic, Title: "Beta", Text: "SECOND", PublishedAt: base.Add(time.Minute), FirstSeenAt: base, LastSyncedAt: base},
		{ID: "zsxq:content:3", Platform: model.PlatformZSXQ, SourceID: sources[1].ID, ExternalID: "3", UpstreamType: "talk", Type: model.ContentDiscussion, Title: "Gamma", Text: "third", PublishedAt: base.Add(2 * time.Minute), FirstSeenAt: base, LastSyncedAt: base},
	}
	for _, content := range contents {
		require.NoError(t, store.ArchiveContent(content, nil))
	}
	tests := []struct {
		name  string
		query PlatformContentQuery
		want  []string
	}{
		{name: "platform", query: PlatformContentQuery{Platform: model.PlatformBilibili}, want: []string{contents[1].ID, contents[0].ID}},
		{name: "source", query: PlatformContentQuery{SourceID: sources[1].ID}, want: []string{contents[2].ID}},
		{name: "case insensitive keyword", query: PlatformContentQuery{Keyword: "second"}, want: []string{contents[1].ID}},
		{name: "half open time", query: PlatformContentQuery{From: base.Add(time.Minute), To: base.Add(2 * time.Minute)}, want: []string{contents[1].ID}},
		{name: "cursor", query: PlatformContentQuery{AfterAt: base.Add(2 * time.Minute), AfterID: contents[2].ID}, want: []string{contents[1].ID, contents[0].ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items, err := store.QueryContents(tt.query)
			require.NoError(t, err)
			ids := make([]string, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ID)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

func newContentStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 93))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

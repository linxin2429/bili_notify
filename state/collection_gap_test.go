package state

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeedGapSamplesAreBoundedWithoutLosingDiscoveries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		count int
	}{{"below cap", 99}, {"at cap", 100}, {"above cap", 250}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 40)
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
			item := CollectionItem{SourceID: "bilibili:up:42", ID: "pending", Kind: "dynamic", Payload: []byte(`{}`)}
			gaps := make([]json.RawMessage, tt.count)
			for i := range gaps {
				gaps[i] = json.RawMessage(fmt.Sprintf(`{"id":%d}`, i))
			}
			require.NoError(t, store.StageFeedPage("100", []CollectionItem{item}, gaps))
			var rows []collectionFeedGap
			require.NoError(t, store.db.Order("rowid DESC").Find(&rows).Error)
			require.Len(t, rows, min(tt.count, 100))
			assert.Equal(t, []byte(gaps[len(gaps)-1]), rows[0].Payload)
			assert.Equal(t, []byte(gaps[max(0, tt.count-100)]), rows[len(rows)-1].Payload)
			// Repeating an already retained sample does not allocate another row.
			require.NoError(t, store.StageFeedPage("100", nil, gaps[len(gaps)-1:]))
			var count int64
			require.NoError(t, store.db.Model(&collectionFeedGap{}).Count(&count).Error)
			assert.Equal(t, int64(min(tt.count, 100)), count)
			pending, err := store.DueCollectionItems("42", "dynamic", time.Now(), 10)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			assert.Equal(t, "pending", pending[0].ID)
		})
	}
}

func TestFeedGapAccountLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*Store) error
		want   int64
	}{
		{"renew same account", func(s *Store) error {
			return s.SaveSession(model.BiliSession{AccountUID: "100", Cookies: map[string]string{"SESSDATA": "renewed"}})
		}, 1},
		{"replace session", func(s *Store) error { return s.SaveSession(model.BiliSession{AccountUID: "200"}) }, 0},
		{"clear session", func(s *Store) error { return s.ClearSession() }, 0},
		{"delete account", func(s *Store) error { return s.DeletePlatformAccount(model.PlatformBilibili) }, 0},
		{"replace platform account", func(s *Store) error {
			return s.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformBilibili, ExternalID: "200", Status: model.AccountConnected})
		}, 0},
		{"update platform status", func(s *Store) error {
			return s.SetPlatformAccountStatus(model.PlatformBilibili, model.AccountInvalid, "expired")
		}, 1},
		{"delete other platform", func(s *Store) error { return s.DeletePlatformAccount(model.PlatformZSXQ) }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 41)
			require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "100"}))
			require.NoError(t, store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "9", Status: model.AccountConnected}))
			require.NoError(t, store.StageFeedPage("100", nil, []json.RawMessage{json.RawMessage(`{"id":"gap"}`)}))
			require.NoError(t, tt.change(store))
			var count int64
			require.NoError(t, store.db.Model(&collectionFeedGap{}).Count(&count).Error)
			assert.Equal(t, tt.want, count)
		})
	}
}

func TestFeedGapCleanupIsAtomicWithAccountDeletion(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 42)
	require.NoError(t, store.StageFeedPage("old", nil, []json.RawMessage{json.RawMessage(`{"id":"old"}`)}))
	require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "100"}))
	var count int64
	require.NoError(t, store.db.Model(&collectionFeedGap{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, store.StageFeedPage("100", nil, []json.RawMessage{json.RawMessage(`{"id":"current"}`)}))
	require.NoError(t, store.db.Exec(`CREATE TRIGGER reject_gap_cleanup BEFORE DELETE ON collection_feed_gaps BEGIN SELECT RAISE(ABORT,'test cleanup failure'); END`).Error)
	require.ErrorContains(t, store.ClearSession(), "test cleanup failure")
	account, err := store.PlatformAccount(model.PlatformBilibili)
	require.NoError(t, err)
	assert.Equal(t, "100", account.ExternalID)
	require.NoError(t, store.db.Model(&collectionFeedGap{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

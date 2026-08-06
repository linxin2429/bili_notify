package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClearSessionAndResetFeed(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 101))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "100", Cookies: map[string]string{"SESSDATA": "session"}}))
	require.NoError(t, store.ClearSession())
	_, err = store.Session()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)

	fixed := time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC)
	require.NoError(t, store.InitializeFeed("100", "baseline", fixed))
	require.NoError(t, store.ResetFeed("100", nil, fixed.Add(time.Minute)))
	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.False(t, feed.Initialized)
	assert.Empty(t, feed.UpdateBaseline)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": model.Followed}, fixed))
	require.NoError(t, store.MarkSpaceSynced("100", "42", fixed))
	require.NoError(t, store.ResetFeed("100", []string{"42"}, fixed.Add(2*time.Minute)))
	ups, err := store.ListUPs()
	require.NoError(t, err)
	require.Len(t, ups, 1)
	assert.Equal(t, model.CollectionRouteSpace, ups[0].CollectionRoute)
}

func TestUnblockChannelRequeuesBlockedDeliveries(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 102))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	channel, err := store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)
	_, err = store.RecordDynamics("42", []model.Dynamic{
		{ID: "blocked", UID: "42", PublishedAt: time.Now()},
		{ID: "pending", UID: "42", PublishedAt: time.Now()},
	}, []string{channel.ID}, DynamicBaselineNone)
	require.NoError(t, err)
	require.NoError(t, store.FailDelivery("blocked:"+channel.ID, true, time.Now().Add(time.Hour), errors.New("blocked"), nil))

	require.NoError(t, store.UnblockChannel(channel.ID))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	states := make(map[string]model.DeliveryState, len(deliveries))
	for _, delivery := range deliveries {
		states[delivery.Dynamic.ID] = delivery.State
	}
	assert.Equal(t, model.DeliveryPending, states["blocked"])
	assert.Equal(t, model.DeliveryPending, states["pending"])
}

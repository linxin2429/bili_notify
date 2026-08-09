package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestPollUPBuildsBaselineThenOutbox(t *testing.T) {
	t.Parallel()
	var request atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := request.Add(1)
		items := dynamicFixture("1", 1700000000)
		if count > 1 {
			items = dynamicFixture("2", 1700000001) + "," + items
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, items)
	}))
	t.Cleanup(server.Close)

	v, err := vault.New(bytes.Repeat([]byte{8}, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Enabled: true}
	require.NoError(t, store.PutUP(up))
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	events := NewEventBus()
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), events, nil)
	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	got, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, got)

	up, err = store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)

	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	got, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "2", got[0].Dynamic.ID)

	beforeIdlePoll := events.Revision()
	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	assert.Equal(t, beforeIdlePoll, events.Revision(), "an idle successful poll should not publish unchanged status or UP data")
}

func TestPollUPBaselinesExistingExclusiveDynamicsOnce(t *testing.T) {
	t.Parallel()
	var request atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		items := exclusiveDynamicFixture("exclusive-old", 1700000000) + "," + dynamicFixture("normal-new", 1700000001)
		if request.Add(1) > 1 {
			items = exclusiveDynamicFixture("exclusive-new", 1700000002) + "," + items
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, items)
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Enabled: true, BaselineReady: true}
	require.NoError(t, store.PutUP(up))
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)

	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "normal-new", deliveries[0].Dynamic.ID)
	up, err = store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.ExclusiveBaselineReady)

	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	ids := []string{deliveries[0].Dynamic.ID, deliveries[1].Dynamic.ID}
	assert.ElementsMatch(t, []string{"normal-new", "exclusive-new"}, ids)
}

func TestPollFeedUsesUpdateGateAndFiltersMonitoredUPs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		updateNum      int
		wantFullFetch  int32
		wantDeliveries int
		wantBaseline   string
	}{
		{name: "no update skips full feed", wantBaseline: "old"},
		{name: "new update filters unrelated author", updateNum: 2, wantFullFetch: 1, wantDeliveries: 1, wantBaseline: "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var fullFetches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/x/polymer/web-dynamic/v1/feed/all/update":
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"update_num":%d}}`, tt.updateNum)
				case "/x/polymer/web-dynamic/v1/feed/all":
					fullFetches.Add(1)
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","update_baseline":"new","update_num":2,"items":[%s,%s]}}`,
						dynamicWithAuthorFixture("new-dynamic", "42", 1700000001),
						`{"id_str":"irrelevant","type":"NEW_TYPE","modules":{"module_author":{"mid":99,"name":"other","pub_ts":1700000000}}}`,
					)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			session := model.BiliSession{AccountUID: "100", AccountName: "account", Cookies: map[string]string{"SESSDATA": "session"}}
			require.NoError(t, store.SaveSession(session))
			up := model.UP{UID: "42", Name: "target", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": model.Followed}, time.Now()))
			require.NoError(t, store.MarkSpaceSynced("100", "42", time.Now()))
			require.NoError(t, store.InitializeFeed("100", "old", time.Now()))

			client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
			client.SetSession(session)
			engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 2), nil, nil)
			engine.setAccount(model.BiliAccount{UID: "100", Name: "account"})
			require.NoError(t, engine.pollFeed(t.Context(), model.BiliAccount{UID: "100", Name: "account"}, []model.UP{up}, []string{"channel"}))

			assert.Equal(t, tt.wantFullFetch, fullFetches.Load())
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, tt.wantDeliveries)
			feed, err := store.FeedState("100")
			require.NoError(t, err)
			assert.Equal(t, tt.wantBaseline, feed.UpdateBaseline)
		})
	}
}

func TestPollFeedIsolatesOneMonitoredUPParseFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/all/update":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"update_num":2}}`)
		case "/x/polymer/web-dynamic/v1/feed/all":
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","update_baseline":"new","update_num":2,"items":[%s,%s]}}`,
				dynamicWithAuthorFixture("good", "42", 1700000001),
				`{"id_str":"bad","type":"NEW_TYPE","modules":{"module_author":{"mid":43,"name":"bad","pub_ts":1700000000}}}`,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	session := model.BiliSession{AccountUID: "100", Cookies: map[string]string{"SESSDATA": "session"}}
	require.NoError(t, store.SaveSession(session))
	ups := []model.UP{
		{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true},
		{UID: "43", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true},
	}
	for _, up := range ups {
		require.NoError(t, store.PutUP(up))
	}
	require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": model.Followed, "43": model.Followed}, time.Now()))
	for _, up := range ups {
		require.NoError(t, store.MarkSpaceSynced("100", up.UID, time.Now()))
	}
	require.NoError(t, store.InitializeFeed("100", "old", time.Now()))

	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	client.SetSession(session)
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 2), nil, nil)
	engine.setAccount(model.BiliAccount{UID: "100"})
	require.NoError(t, engine.pollFeed(t.Context(), model.BiliAccount{UID: "100"}, ups, []string{"channel"}))

	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.Equal(t, "new", feed.UpdateBaseline)
	relations, err := store.FollowRelations("100")
	require.NoError(t, err)
	assert.True(t, relations["42"].SpaceSynced)
	assert.False(t, relations["43"].SpaceSynced)
	badUP, err := store.UP("43")
	require.NoError(t, err)
	assert.Equal(t, 1, badUP.ConsecutiveFail)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "good", deliveries[0].Dynamic.ID)
}

func dynamicFixture(id string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"tester","pub_ts":%d},"module_dynamic":{"desc":{"text":"hello"},"major":null}}}`, id, timestamp)
}

func dynamicWithAuthorFixture(id, uid string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"mid":%q,"name":"tester","pub_ts":%d},"module_dynamic":{"desc":{"text":"hello"},"major":null}}}`, id, uid, timestamp)
}

func exclusiveDynamicFixture(id string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_DRAW","basic":{"is_only_fans":true},"modules":{"module_author":{"name":"tester","pub_ts":%d},"module_dynamic":{"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/exclusive.jpg"}]}}}}}`, id, timestamp)
}

func TestRetryDelayBounds(t *testing.T) {
	t.Parallel()
	delays := model.DefaultRuntimeSettings().DeliveryRetryDelaysSec
	for range 100 {
		delay := retryDelay(0, delays)
		assert.GreaterOrEqual(t, delay, 2500*time.Millisecond)
		assert.Less(t, delay, 5*time.Second)
	}
}

func TestPollUPLogsSchemaFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"tester","pub_ts":"invalid"}}}]}}`)
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Name: "configured name", Enabled: true}
	require.NoError(t, store.PutUP(up))
	var logs bytes.Buffer
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewJSONHandler(&logs, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
	updated, err := store.UP(up.UID)
	require.NoError(t, err)
	assert.Equal(t, 1, updated.ConsecutiveFail)
	for _, expected := range []string{`"msg":"Bilibili UP poll failed"`, `"event":"bilibili.up.poll_completed"`, `"up_uid":"42"`, `"up_name":"configured name"`, `"error_kind":"schema"`} {
		assert.Contains(t, logs.String(), expected)
	}
}

func mustTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return v
}

func testSettings(pollSec int, rate float64, concurrency int) model.RuntimeSettings {
	settings := model.DefaultRuntimeSettings()
	settings.PollIntervalSec = pollSec
	settings.RequestRate = rate
	settings.RequestConcurrency = concurrency
	return settings
}

func TestApplySettingsHotReloads(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 2, 4), NewEventBus(), nil)
	_, changed := engine.settingsSnapshot()
	updated := testSettings(120, 1.5, 8)
	updated.RelationRefreshSec = 900
	updated.DeliveryConcurrency = 12
	engine.ApplySettings(updated)
	assert.Equal(t, updated, engine.Settings())
	select {
	case <-changed:
	default:
		require.Fail(t, "settings change was not broadcast")
	}
}

func TestStatusReadyUsesPollIntervalWindow(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.PutUP(model.UP{UID: "1", Enabled: true, BaselineReady: true}))
	_, err = store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)

	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(180, 2, 4), nil, nil)
	engine.authValid.Store(true)
	engine.lastSuccess.Store(time.Now().Add(-150 * time.Second).Unix())

	status, err := engine.Status()
	require.NoError(t, err)
	assert.True(t, status.Ready, "150s lag should still be ready when poll_interval is 180s")

	engine.lastSuccess.Store(time.Now().Add(-7 * time.Minute).Unix())
	status, err = engine.Status()
	require.NoError(t, err)
	assert.False(t, status.Ready, "beyond 2*poll_interval should be unready")
}

func TestClockDerivedStatusPublishesOnlyOnBoundaryChanges(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.PutUP(model.UP{UID: "1", Enabled: true, BaselineReady: true}))
	_, err = store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)

	events := NewEventBus()
	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 2, 4), events, nil)
	engine.authValid.Store(true)
	engine.lastSuccess.Store(time.Now().Unix())
	engine.riskUntil.Store(time.Now().Add(5 * time.Minute).Unix())

	require.NoError(t, engine.publishClockStatusIfChanged())
	beforeUnchanged := events.Revision()
	require.NoError(t, engine.publishClockStatusIfChanged())
	assert.Equal(t, beforeUnchanged, events.Revision())

	engine.riskUntil.Store(time.Now().Add(-time.Second).Unix())
	require.NoError(t, engine.publishClockStatusIfChanged())
	assert.Equal(t, beforeUnchanged+1, events.Revision(), "risk expiry must publish the newly derived status")
}

func TestPollUPPublishesCommittedOutboxBeforeLaterBookkeepingFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, dynamicFixture("new", 1700000000))
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	events := NewEventBus()
	subscription := events.Subscribe()
	t.Cleanup(subscription.Close)
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), events, nil)

	err = engine.pollUP(t.Context(), model.UP{
		UID: "42", Name: "tester", Enabled: true,
		BaselineReady: true, ExclusiveBaselineReady: true,
	}, []string{"channel"})
	require.ErrorIs(t, err, state.ErrNotFound)
	deliveries, listErr := store.ListDeliveries(0)
	require.NoError(t, listErr)
	require.Len(t, deliveries, 1)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	topics, _, eventErr := subscription.Next(ctx)
	require.NoError(t, eventErr)
	assert.NotZero(t, topics&TopicStatus)
	assert.NotZero(t, topics&TopicDeliveries)
}

func TestDispatchOnceDoesNotPublishWhenQueueIsIdle(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	events := NewEventBus()
	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 2, 4), events, nil)

	before := events.Revision()
	require.NoError(t, engine.dispatchOnce(t.Context()))
	assert.Equal(t, before, events.Revision())
}

func TestDispatchOncePublishesMinimalTopicsAfterDeliveryChanges(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.RecordDynamics("42", []model.Dynamic{{
		ID: "dynamic", UID: "42", UPName: "tester", Type: "DYNAMIC_TYPE_WORD",
		PublishedAt: time.Now(), Summary: "hello", URL: "https://t.bilibili.com/1",
	}}, []string{"missing-channel"}, state.DynamicBaselineNone)
	require.NoError(t, err)
	require.Equal(t, 1, created)

	events := NewEventBus()
	subscription := events.Subscribe()
	t.Cleanup(subscription.Close)
	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 2, 4), events, nil)
	require.NoError(t, engine.dispatchOnce(t.Context()))

	topics, _, err := subscription.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, TopicStatus|TopicDeliveries, topics)
	assert.Zero(t, topics&TopicChannels)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.DeliveryBlocked, deliveries[0].State)
}

func TestWithNotificationHTTPClient(t *testing.T) {
	t.Parallel()
	client := &http.Client{}
	engine := &Engine{}
	WithNotificationHTTPClient(client)(engine)
	assert.Same(t, client, engine.notificationClient)
}

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestCollectionItemRecoverySurvivesRestartAndSeenFrontier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		card     func(bool) string
		baseline bool
		feed     bool
	}{
		{name: "feed article", feed: true, card: func(bool) string { return articleDynamicFixture("bad", 1700000002) }},
		{name: "article", card: func(bool) string { return articleDynamicFixture("bad", 1700000002) }},
		{name: "forwarded article", card: func(bool) string { return forwardedArticleFixture("bad", "original", 1700000002) }},
		{name: "schema changes upstream", card: func(recovered bool) string {
			if recovered {
				return dynamicFixture("bad", 1700000002)
			}
			return `{"id_str":"bad","type":"NEW_TYPE","modules":{"module_author":{"pub_ts":1700000002}}}`
		}},
		{name: "baseline retry stays silent", baseline: true, card: func(bool) string { return articleDynamicFixture("bad", 1700000002) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			var opusCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/x/polymer/web-dynamic/v1/feed/space":
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s,%s],"has_more":false}}`, dynamicFixture("good", 1700000003), tt.card(recovered.Load()))
				case "/x/polymer/web-dynamic/v1/feed/all/update":
					_, _ = io.WriteString(w, `{"code":0,"data":{"update_num":2}}`)
				case "/x/polymer/web-dynamic/v1/feed/all":
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s,%s],"update_num":2,"update_baseline":"new"}}`, dynamicWithAuthorFixture("good", "42", 1700000003), tt.card(recovered.Load()))
				case "/x/polymer/web-dynamic/v1/opus/detail":
					opusCalls.Add(1)
					if !recovered.Load() {
						_, _ = io.WriteString(w, `{"code":4101131,"message":"unavailable"}`)
						return
					}
					_, _ = io.WriteString(w, articleOpusDetail(r.URL.Query().Get("id"), "full body", "https://i0.hdslb.com/body.jpg"))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			path := filepath.Join(t.TempDir(), "data.db")
			v := mustTestVault(t)
			store, err := state.Open(t.Context(), path, v)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			up := model.UP{UID: "42", Enabled: true, BaselineReady: !tt.baseline, ExclusiveBaselineReady: !tt.baseline}
			require.NoError(t, store.PutUP(up))
			putServiceTestChannel(t, store)
			newEngine := func() *Engine {
				return NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 2), nil, nil)
			}
			engine := newEngine()
			require.NoError(t, store.InitializeFeed("100", "old", time.Now()))
			poll := func() error {
				if tt.feed {
					return engine.pollFeed(t.Context(), model.BiliAccount{UID: "100"}, []model.UP{up})
				}
				return engine.pollUP(t.Context(), up)
			}
			require.NoError(t, poll())
			seen, err := store.Seen("42", "good")
			require.NoError(t, err)
			assert.True(t, seen)
			seen, err = store.Seen("42", "bad")
			require.NoError(t, err)
			assert.False(t, seen)
			queued, err := store.DueCollectionItems("42", "dynamic", time.Now().Add(2*time.Hour), 100)
			require.NoError(t, err)
			require.Len(t, queued, 1)
			assert.Equal(t, 1, queued[0].Attempts)
			assert.Greater(t, queued[0].NextAt, time.Now().Unix())
			before := opusCalls.Load()
			require.NoError(t, store.Close())
			store, err = state.Open(t.Context(), path, v)
			require.NoError(t, err)
			engine = newEngine()
			up, err = store.UP("42")
			require.NoError(t, err)
			require.NoError(t, poll())
			assert.Equal(t, before, opusCalls.Load(), "restart and repeated discovery must preserve retry backoff")
			recovered.Store(true)
			require.NoError(t, store.FailCollectionItem(queued[0], time.Now().Add(-2*time.Hour), errors.New("test retry now")))
			require.NoError(t, poll())
			seen, err = store.Seen("42", "bad")
			require.NoError(t, err)
			assert.True(t, seen, "newer seen items must not skip the independent retry")
			queued, err = store.DueCollectionItems("42", "dynamic", time.Now().Add(2*time.Hour), 100)
			require.NoError(t, err)
			assert.Empty(t, queued)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			want := 2
			if tt.baseline {
				want = 0
			}
			assert.Len(t, deliveries, want)
			require.NoError(t, poll())
			deliveries, err = store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, want, "replay must not duplicate notifications")
		})
	}
}

func TestSpaceScanResumesAcrossBudgetAndRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		brokenCursor bool
	}{
		{name: "page budget"}, {name: "invalid cursor then recovery", brokenCursor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, next := "new", "second"
				switch r.URL.Query().Get("offset") {
				case "second":
					id, next = "middle", "third"
				case "third":
					id, next = "old", ""
				}
				if tt.brokenCursor && !recovered.Load() {
					next = ""
				}
				_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s],"offset":%q,"has_more":%t}}`, dynamicFixture(id, 1700000000), next, id != "old")
			}))
			t.Cleanup(server.Close)
			path, v := filepath.Join(t.TempDir(), "data.db"), mustTestVault(t)
			store, err := state.Open(t.Context(), path, v)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			up := model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			putServiceTestChannel(t, store)
			settings := testSettings(30, 1000, 2)
			settings.BilibiliMaxDynamicPages = 1
			newEngine := func() *Engine {
				return NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil)
			}
			require.NoError(t, newEngine().pollUP(t.Context(), up))
			scan, err := store.CollectionScan("42")
			require.NoError(t, err)
			assert.True(t, scan.Active)
			if tt.brokenCursor {
				scan.NextAt = 0
				require.NoError(t, store.StageCollectionPage(nil, &scan))
			}
			require.NoError(t, store.Close())
			store, err = state.Open(t.Context(), path, v)
			require.NoError(t, err)
			recovered.Store(true)
			engine := newEngine()
			for range 3 {
				require.NoError(t, engine.pollUP(t.Context(), up))
			}
			for _, id := range []string{"new", "middle", "old"} {
				seen, err := store.Seen("42", id)
				require.NoError(t, err)
				assert.True(t, seen, id)
			}
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, 3)
			scan, err = store.CollectionScan("42")
			require.NoError(t, err)
			assert.False(t, scan.Active)
			assert.Empty(t, scan.Offset)
		})
	}
}

func TestFeedUnassignableCardRetainsHealthyContentsAndResynchronizes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/all/update":
			_, _ = io.WriteString(w, `{"code":0,"data":{"update_num":2}}`)
		case "/x/polymer/web-dynamic/v1/feed/all":
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s,{"id_str":"missing-author"}],"update_num":2,"update_baseline":"new"}}`, dynamicWithAuthorFixture("good", "42", 1700000003))
		case "/x/polymer/web-dynamic/v1/feed/space":
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s,%s]}}`, dynamicFixture("good", 1700000003), dynamicFixture("missing-author", 1700000002))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	store := openServiceTestStore(t)
	up := model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
	require.NoError(t, store.PutUP(up))
	putServiceTestChannel(t, store)
	require.NoError(t, store.InitializeFeed("100", "old", time.Now()))
	require.NoError(t, store.MarkSpaceSynced("100", "42", time.Now()))
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 2), nil, nil)
	require.NoError(t, engine.pollFeed(t.Context(), model.BiliAccount{UID: "100"}, []model.UP{up}))
	seen, err := store.Seen("42", "good")
	require.NoError(t, err)
	assert.True(t, seen)
	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.False(t, feed.Initialized)
	require.NoError(t, engine.pollUP(t.Context(), up))
	seen, err = store.Seen("42", "missing-author")
	require.NoError(t, err)
	assert.True(t, seen)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Len(t, deliveries, 2)
}

func TestDiscoveryInvalidIdentityHasDurableKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing id", raw: `{"type":"NEW_TYPE"}`},
		{name: "non string id", raw: `{"id_str":42}`},
		{name: "null", raw: `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openServiceTestStore(t)
			up := model.UP{UID: "42", Enabled: true}
			require.NoError(t, store.PutUP(up))
			item := discoveryItem(up, json.RawMessage(tt.raw))
			assert.NotEmpty(t, item.ID)
			require.NoError(t, store.StageCollectionPage([]state.CollectionItem{item}, nil))
			require.NoError(t, store.StageCollectionPage([]state.CollectionItem{item}, nil))
			rows, err := store.DueCollectionItems("42", "dynamic", time.Now(), 100)
			require.NoError(t, err)
			assert.Len(t, rows, 1)
		})
	}
}

func TestExclusiveBaselineIsPreservedAcrossScanPages(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, next := "exclusive-newer", "next"
		if r.URL.Query().Get("offset") != "" {
			id, next = "exclusive-older", ""
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[%s],"has_more":%t,"offset":%q}}`, exclusiveDynamicFixture(id, 1700000000), next != "", next)
	}))
	t.Cleanup(server.Close)
	store := openServiceTestStore(t)
	up := model.UP{UID: "42", Enabled: true, BaselineReady: true}
	require.NoError(t, store.PutUP(up))
	putServiceTestChannel(t, store)
	settings := testSettings(30, 1000, 2)
	settings.BilibiliMaxDynamicPages = 1
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil)
	require.NoError(t, engine.pollUP(t.Context(), up))
	up, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.ExclusiveBaselineReady)
	require.NoError(t, engine.pollUP(t.Context(), up))
	seen, err := store.Seen("42", "exclusive-older")
	require.NoError(t, err)
	assert.True(t, seen)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries, "continuation pages retain the original silent baseline mode")
}

func TestFeedCycleCountsEachUPFailureOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		feedFailure, lateAuth bool
		healthyFailures       int
	}{
		{name: "feed and item failures", feedFailure: true, healthyFailures: 1},
		{name: "item failures only", healthyFailures: 0},
		{name: "later item invalidates authentication", lateAuth: true, healthyFailures: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var opusCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/polymer/web-dynamic/v1/feed/all/update" {
					if tt.feedFailure {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"update_num":0}}`)
					return
				}
				if r.URL.Path == "/x/polymer/web-dynamic/v1/opus/detail" {
					opusCalls.Add(1)
					_, _ = io.WriteString(w, `{"code":-101,"message":"login expired"}`)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			ups := []model.UP{{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}, {UID: "43", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}}
			for _, up := range ups {
				require.NoError(t, store.PutUP(up))
			}
			require.NoError(t, store.InitializeFeed("100", "baseline", time.Now()))
			bad := discoveryItem(ups[0], json.RawMessage(`{"id_str":"bad","type":"UNKNOWN_TYPE"}`))
			items := []state.CollectionItem{bad}
			if tt.lateAuth {
				items = append(items, discoveryItem(ups[1], json.RawMessage(articleDynamicWithAuthorFixture("auth", "43", 1700000000))))
			}
			require.NoError(t, store.StageCollectionPage(items, nil))
			engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 2), nil, nil)
			require.NoError(t, engine.pollFeed(t.Context(), model.BiliAccount{UID: "100"}, ups))
			if tt.lateAuth {
				assert.Equal(t, int32(1), opusCalls.Load())
			}
			badUP, err := store.UP("42")
			require.NoError(t, err)
			assert.Equal(t, 1, badUP.ConsecutiveFail)
			otherUP, err := store.UP("43")
			require.NoError(t, err)
			assert.Equal(t, tt.healthyFailures, otherUP.ConsecutiveFail)
			pending, err := store.DueCollectionItems("42", "dynamic", time.Now().Add(time.Hour), 10)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			assert.Equal(t, 1, pending[0].Attempts)
		})
	}
}

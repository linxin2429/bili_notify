package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

func TestPollUPPaginationInvariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		maxPages     int
		seedSeen     bool
		page         func(offset string) (items, next string, hasMore bool)
		wantRequests int32
		wantIDs      []string
		wantUnseen   []string
		wantFailure  bool
	}{
		{
			name: "collects two pages", maxPages: 2, wantRequests: 2, wantIDs: []string{"new-1", "new-2"},
			page: func(offset string) (string, string, bool) {
				if offset == "" {
					return dynamicFixture("new-2", 1700000002), "next", true
				}
				return dynamicFixture("new-1", 1700000001), "", false
			},
		},
		{
			name: "seen frontier stops page and older items", maxPages: 2, seedSeen: true, wantRequests: 1, wantIDs: []string{"seen", "new"},
			page: func(string) (string, string, bool) {
				return dynamicFixture("new", 1700000003) + "," + dynamicFixture("seen", 1700000002) + "," + dynamicFixture("older", 1700000001), "next", true
			},
		},
		{
			name: "empty offset rejects partial page", maxPages: 2, wantRequests: 1, wantUnseen: []string{"partial"}, wantFailure: true,
			page: func(string) (string, string, bool) {
				return dynamicFixture("partial", 1700000001), "", true
			},
		},
		{
			name: "repeated offset rejects partial pages", maxPages: 3, wantRequests: 2, wantUnseen: []string{"partial-1", "partial-2"}, wantFailure: true,
			page: func(offset string) (string, string, bool) {
				if offset == "" {
					return dynamicFixture("partial-1", 1700000002), "same", true
				}
				return dynamicFixture("partial-2", 1700000001), "same", true
			},
		},
		{
			name: "page limit rejects partial history", maxPages: 2, wantRequests: 2, wantUnseen: []string{"partial-1", "partial-2"}, wantFailure: true,
			page: func(offset string) (string, string, bool) {
				if offset == "" {
					return dynamicFixture("partial-1", 1700000002), "second", true
				}
				return dynamicFixture("partial-2", 1700000001), "third", true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				items, offset, hasMore := tt.page(r.URL.Query().Get("offset"))
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":%t,"offset":%q,"items":[%s]}}`, hasMore, offset, items)
			}))
			t.Cleanup(server.Close)
			store := openServiceTestStore(t)
			up := model.UP{UID: "42", Name: "UP", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			if tt.seedSeen {
				_, err := store.RecordDynamics(up.UID, []model.Dynamic{{ID: "seen", UID: up.UID, Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Unix(1700000002, 0)}}, []string{"channel"}, state.DynamicBaselineNone)
				require.NoError(t, err)
				deliveries, err := store.ListDeliveries(0)
				require.NoError(t, err)
				require.Len(t, deliveries, 1)
				require.NoError(t, store.CompleteDelivery(deliveries[0].ID))
			}
			settings := testSettings(30, 1000, 2)
			settings.BilibiliMaxDynamicPages = tt.maxPages
			engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil)

			require.NoError(t, engine.pollUP(t.Context(), up, []string{"channel"}))
			assert.Equal(t, tt.wantRequests, requests.Load())
			records, total, err := store.QueryDynamics(state.ContentQuery{UID: up.UID, Limit: 100})
			require.NoError(t, err)
			assert.Equal(t, len(tt.wantIDs), total)
			ids := make([]string, 0, len(records))
			for _, record := range records {
				ids = append(ids, record.ID)
			}
			assert.ElementsMatch(t, tt.wantIDs, ids)
			updated, err := store.UP(up.UID)
			require.NoError(t, err)
			if tt.wantFailure {
				assert.Equal(t, 1, updated.ConsecutiveFail)
				assert.Empty(t, records)
				deliveries, listErr := store.ListDeliveries(0)
				require.NoError(t, listErr)
				assert.Empty(t, deliveries)
				for _, id := range tt.wantUnseen {
					seen, seenErr := store.Seen(up.UID, id)
					require.NoError(t, seenErr)
					assert.False(t, seen, "partial dynamic %s must not be committed", id)
				}
			} else {
				assert.Zero(t, updated.ConsecutiveFail)
			}
		})
	}
}

func TestPollFeedPaginationInvariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		maxPages      int
		updateNum     int
		page          func(offset string) (items, next string, hasMore bool)
		wantBaseline  string
		wantDynamics  int
		wantSpaceSync bool
	}{
		{
			name: "collects exactly two pages", maxPages: 2, updateNum: 2, wantBaseline: "new", wantDynamics: 2, wantSpaceSync: true,
			page: func(offset string) (string, string, bool) {
				if offset == "" {
					return dynamicWithAuthorFixture("feed-2", "42", 1700000002), "next", true
				}
				return dynamicWithAuthorFixture("feed-1", "42", 1700000001), "", false
			},
		},
		{
			name: "short feed does not advance baseline", maxPages: 2, updateNum: 2, wantBaseline: "old", wantSpaceSync: true,
			page: func(string) (string, string, bool) {
				return dynamicWithAuthorFixture("partial", "42", 1700000001), "", false
			},
		},
		{
			name: "repeated offset does not advance baseline", maxPages: 3, updateNum: 3, wantBaseline: "old", wantSpaceSync: true,
			page: func(offset string) (string, string, bool) {
				id := "partial-1"
				if offset != "" {
					id = "partial-2"
				}
				return dynamicWithAuthorFixture(id, "42", 1700000001), "same", true
			},
		},
		{
			name: "page overflow resets feed for resynchronization", maxPages: 2, updateNum: 3, wantBaseline: "",
			page: func(offset string) (string, string, bool) {
				if offset == "" {
					return dynamicWithAuthorFixture("partial-1", "42", 1700000002), "second", true
				}
				return dynamicWithAuthorFixture("partial-2", "42", 1700000001), "third", true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/x/polymer/web-dynamic/v1/feed/all/update":
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"update_num":%d}}`, tt.updateNum)
				case "/x/polymer/web-dynamic/v1/feed/all":
					items, offset, hasMore := tt.page(r.URL.Query().Get("offset"))
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":%t,"offset":%q,"update_baseline":"new","update_num":%d,"items":[%s]}}`, hasMore, offset, tt.updateNum, items)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			store := openServiceTestStore(t)
			account := model.BiliAccount{UID: "100", Name: "account"}
			up := model.UP{UID: "42", Name: "UP", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			require.NoError(t, store.PutFollowRelations(account.UID, map[string]model.FollowState{up.UID: model.Followed}, time.Now()))
			require.NoError(t, store.MarkSpaceSynced(account.UID, up.UID, time.Now()))
			require.NoError(t, store.InitializeFeed(account.UID, "old", time.Now()))
			settings := testSettings(30, 1000, 2)
			settings.BilibiliMaxDynamicPages = tt.maxPages
			engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil)
			engine.setAccount(account)

			require.NoError(t, engine.pollFeed(t.Context(), account, []model.UP{up}, []string{"channel"}))
			feed, err := store.FeedState(account.UID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBaseline, feed.UpdateBaseline)
			_, total, err := store.QueryDynamics(state.ContentQuery{UID: up.UID})
			require.NoError(t, err)
			assert.Equal(t, tt.wantDynamics, total)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Len(t, deliveries, tt.wantDynamics)
			relations, err := store.FollowRelations(account.UID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSpaceSync, relations[up.UID].SpaceSynced)
		})
	}
}

func TestCommentPaginationMarksTruncatedThreadsIncomplete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rootCount  int
		childCount int
		rootPages  int
		replyPages int
		childUP    bool
	}{
		{name: "root page cap", rootCount: 21, rootPages: 1, replyPages: 1},
		{name: "child page cap", rootCount: 1, childCount: 21, rootPages: 1, replyPages: 1, childUP: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/v2/reply" {
					mid, rcount := "42", tt.childCount
					if tt.childUP {
						mid = "7"
					}
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"page":{"num":1,"size":20,"count":%d},"replies":[{"rpid_str":"root","root_str":"0","parent_str":"0","ctime":1700000000,"rcount":%d,"member":{"mid":%q,"uname":"member"},"content":{"message":"root"}}]}}`, tt.rootCount, rcount, mid)
					return
				}
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"page":{"num":1,"size":20,"count":%d},"replies":[{"rpid_str":"child","root_str":"root","parent_str":"root","ctime":1700000001,"member":{"mid":"42","uname":"UP"},"content":{"message":"child"}}]}}`, tt.childCount)
			}))
			t.Cleanup(server.Close)
			store := openServiceTestStore(t)
			target := model.CommentTarget{UID: "42", UPName: "UP", DynamicID: "dynamic", CommentType: 11, CommentOID: "oid", BaselineReady: true}
			require.NoError(t, store.PutCommentTargets(target.UID, []model.CommentTarget{target}))
			engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 2), nil, nil)

			require.NoError(t, engine.pollCommentTarget(t.Context(), target, []string{"channel"}))
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			require.NotNil(t, deliveries[0].Comment)
			assert.True(t, deliveries[0].Comment.Incomplete)
		})
	}
}

func TestCommentPaginationCollectsMultipleRootAndChildPages(t *testing.T) {
	t.Parallel()
	var rootRequests atomic.Int32
	var childRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		if r.URL.Path == "/x/v2/reply" {
			rootRequests.Add(1)
			reply := `{"rpid_str":"root","root_str":"0","parent_str":"0","ctime":1700000000,"rcount":21,"member":{"mid":"7","uname":"viewer"},"content":{"message":"root"}}`
			if page == 2 {
				reply = `{"rpid_str":"up-root","root_str":"0","parent_str":"0","ctime":1700000002,"rcount":0,"member":{"mid":"42","uname":"UP"},"content":{"message":"root reply"}}`
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"page":{"num":%d,"size":20,"count":21},"replies":[%s]}}`, page, reply)
			return
		}
		childRequests.Add(1)
		reply := `{"rpid_str":"viewer-child","root_str":"root","parent_str":"root","ctime":1700000000,"member":{"mid":"7","uname":"viewer"},"content":{"message":"child"}}`
		if page == 2 {
			reply = `{"rpid_str":"up-child","root_str":"root","parent_str":"viewer-child","ctime":1700000001,"member":{"mid":"42","uname":"UP"},"content":{"message":"reply"}}`
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"page":{"num":%d,"size":20,"count":21},"replies":[%s]}}`, page, reply)
	}))
	t.Cleanup(server.Close)
	store := openServiceTestStore(t)
	target := model.CommentTarget{UID: "42", UPName: "UP", DynamicID: "dynamic", CommentType: 11, CommentOID: "oid", BaselineReady: true}
	require.NoError(t, store.PutCommentTargets(target.UID, []model.CommentTarget{target}))
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 2), nil, nil)

	require.NoError(t, engine.pollCommentTarget(t.Context(), target, []string{"channel"}))
	assert.Equal(t, int32(2), rootRequests.Load())
	assert.Equal(t, int32(2), childRequests.Load())
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	for _, delivery := range deliveries {
		require.NotNil(t, delivery.Comment)
		assert.False(t, delivery.Comment.Incomplete)
	}
}

func TestDispatchConcurrencyUsesHotReloadedSetting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		initial     int
		hotReloaded int
	}{
		{name: "initial limit", initial: 2},
		{name: "hot reloaded limit", initial: 1, hotReloaded: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gate := make(chan struct{})
			started := make(chan struct{}, 10)
			var active atomic.Int32
			var maximum atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				current := active.Add(1)
				for {
					observed := maximum.Load()
					if current <= observed || maximum.CompareAndSwap(observed, current) {
						break
					}
				}
				started <- struct{}{}
				<-gate
				active.Add(-1)
				_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
			}))
			t.Cleanup(server.Close)
			store := openServiceTestStore(t)
			channel, err := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": server.URL}})
			require.NoError(t, err)
			for index := 0; index < 6; index++ {
				_, err := store.RecordDynamics("42", []model.Dynamic{{ID: fmt.Sprintf("concurrency-%d", index), UID: "42", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()}}, []string{channel.ID}, state.DynamicBaselineNone)
				require.NoError(t, err)
			}
			settings := testSettings(30, 1000, 2)
			settings.DeliveryConcurrency = tt.initial
			engine := NewEngine(store, bilibili.New(nil, "test"), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil, WithNotificationHTTPClient(server.Client()))
			want := tt.initial
			if tt.hotReloaded > 0 {
				updated := settings
				updated.DeliveryConcurrency = tt.hotReloaded
				engine.ApplySettings(updated)
				want = tt.hotReloaded
			}
			done := make(chan error, 1)
			go func() { done <- engine.dispatchOnce(t.Context()) }()
			for range want {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					require.FailNow(t, "dispatcher did not reach expected concurrency")
				}
			}
			select {
			case <-started:
				require.FailNow(t, "dispatcher exceeded concurrency limit")
			case <-time.After(50 * time.Millisecond):
			}
			close(gate)
			require.NoError(t, <-done)
			assert.Equal(t, int32(want), maximum.Load())
		})
	}
}

func TestDispatchOnceCapsOneCycleAtFiftyDeliveries(t *testing.T) {
	t.Parallel()
	store := openServiceTestStore(t)
	for index := 0; index < 60; index++ {
		_, err := store.RecordDynamics("42", []model.Dynamic{{
			ID: fmt.Sprintf("batch-%d", index), UID: "42", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(),
		}}, []string{"removed-channel"}, state.DynamicBaselineNone)
		require.NoError(t, err)
	}
	settings := testSettings(30, 1000, 4)
	settings.DeliveryConcurrency = 8
	engine := NewEngine(store, bilibili.New(nil, "test"), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil)

	require.NoError(t, engine.dispatchOnce(t.Context()))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 60)
	states := make(map[model.DeliveryState]int)
	for _, delivery := range deliveries {
		states[delivery.State]++
	}
	assert.Equal(t, 50, states[model.DeliveryBlocked])
	assert.Equal(t, 10, states[model.DeliveryPending])
}

func TestBacklogAlertEntersAndRecovers(t *testing.T) {
	t.Parallel()
	var messagesMu sync.Mutex
	messages := make([]string, 0, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			messagesMu.Lock()
			messages = append(messages, string(body))
			messagesMu.Unlock()
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(server.Close)
	store := openServiceTestStore(t)
	channel, err := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": server.URL}})
	require.NoError(t, err)
	for index := 0; index < 2; index++ {
		_, err := store.RecordDynamics("42", []model.Dynamic{{
			ID: fmt.Sprintf("backlog-%d", index), UID: "42", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(), Summary: "content",
		}}, []string{channel.ID}, state.DynamicBaselineNone)
		require.NoError(t, err)
	}
	settings := testSettings(30, 1000, 2)
	settings.BacklogAlertCount = 1
	settings.BacklogAlertAgeSec = 3600
	engine := NewEngine(store, bilibili.New(nil, "test"), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), settings, nil, nil, WithNotificationHTTPClient(server.Client()))

	for range 4 {
		require.NoError(t, engine.dispatchOnce(t.Context()))
	}
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	assert.False(t, engine.backlogAlerted.Load())
	messagesMu.Lock()
	joined := strings.Join(messages, "\n")
	messagesMu.Unlock()
	assert.Contains(t, joined, "通知队列发生积压")
	assert.Contains(t, joined, "通知队列积压已恢复")
}

func TestPollUPHonorsRequestTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		timeout   time.Duration
		cancelNow bool
	}{
		{name: "request timeout", timeout: 25 * time.Millisecond},
		{name: "caller cancellation", timeout: time.Second, cancelNow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requestCanceled := make(chan struct{}, 1)
			client := &http.Client{Transport: serviceRoundTripper(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				requestCanceled <- struct{}{}
				return nil, request.Context().Err()
			})}
			t.Cleanup(client.CloseIdleConnections)
			store := openServiceTestStore(t)
			up := model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			engine := NewEngine(store, bilibili.New(client, "test", bilibili.WithBaseURLs("https://api.invalid", "https://passport.invalid")), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 1000, 1), nil, nil)
			engine.httpTimeout = tt.timeout
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.cancelNow {
				cancel()
			}
			err := engine.pollUP(ctx, up, []string{"channel"})
			if tt.cancelNow {
				require.ErrorIs(t, err, context.Canceled)
				return
			}
			require.NoError(t, err)
			select {
			case <-requestCanceled:
			case <-time.After(time.Second):
				require.FailNow(t, "HTTP request context was not canceled")
			}
			updated, err := store.UP(up.UID)
			require.NoError(t, err)
			assert.Equal(t, 1, updated.ConsecutiveFail)
		})
	}
}

func TestRetryDelayUsesFiveStagesAndSaturates(t *testing.T) {
	t.Parallel()
	delays := model.DeliveryRetryDelays{2, 4, 8, 16, 32}
	tests := []struct {
		attempt int
		stage   int
	}{
		{attempt: 0, stage: 0}, {attempt: 1, stage: 1}, {attempt: 2, stage: 2},
		{attempt: 3, stage: 3}, {attempt: 4, stage: 4}, {attempt: 5, stage: 4},
		{attempt: 100, stage: 4},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.attempt), func(t *testing.T) {
			t.Parallel()
			upper := time.Duration(delays[tt.stage]) * time.Second
			for range 20 {
				got := retryDelay(tt.attempt, delays)
				assert.GreaterOrEqual(t, got, upper/2)
				assert.Less(t, got, upper)
			}
		})
	}
}

func BenchmarkDeliveryMessage(b *testing.B) {
	delivery := model.Delivery{Dynamic: model.Dynamic{
		ID: "dynamic", UID: "42", UPName: "benchmark", Type: "DYNAMIC_TYPE_WORD",
		Summary: "representative notification body", URL: "https://t.bilibili.com/1",
		Media: []model.DynamicMedia{{URL: "https://i0.hdslb.com/image.jpg"}},
	}}
	b.ReportAllocs()
	for b.Loop() {
		_, _, err := deliveryMessage(delivery)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func openServiceTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

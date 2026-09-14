package service

import (
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

func TestPollUPEnrichesArticleBodyAndImages(t *testing.T) {
	t.Parallel()
	var spaceFetches, opusFetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/frontend/finger/spi" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"test-device"}}`)
			return
		}
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/space":
			spaceFetches.Add(1)
			items := articleDynamicFixture("article-old", 1700000000)
			if spaceFetches.Load() > 1 {
				items = articleDynamicFixture("article-new", 1700000001) + "," + items
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, items)
		case "/x/polymer/web-dynamic/v1/opus/detail":
			opusFetches.Add(1)
			id := r.URL.Query().Get("id")
			_, _ = io.WriteString(w, articleOpusDetail(id, "完整专栏正文", "https://i0.hdslb.com/body.jpg"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Enabled: true}
	require.NoError(t, store.PutUP(up))
	putServiceTestChannel(t, store)
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)

	require.NoError(t, engine.pollUP(t.Context(), up))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	assert.Equal(t, int32(1), opusFetches.Load())
	content, attachments, err := store.Content(model.ContentID(model.PlatformBilibili, "article-old"))
	require.NoError(t, err)
	assert.Equal(t, "完整专栏正文", content.Text)
	require.Len(t, attachments, 1)
	assert.Equal(t, "i0.hdslb.com", attachments[0].RemoteHost)
	assert.Equal(t, 10, attachments[0].Width)
	assert.Equal(t, 20, attachments[0].Height)

	up, err = store.UP("42")
	require.NoError(t, err)
	require.NoError(t, engine.pollUP(t.Context(), up))
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "完整专栏正文", deliveries[0].Dynamic.Description)
	require.Len(t, deliveries[0].Dynamic.Media, 1)
	assert.Equal(t, model.DynamicMediaImage, deliveries[0].Dynamic.Media[0].Kind)
	assert.Equal(t, "https://i0.hdslb.com/body.jpg", deliveries[0].Dynamic.Media[0].URL)
	assert.Equal(t, int32(2), opusFetches.Load())
}

func TestPollUPDoesNotRecordArticleWhenDetailFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "schema", body: `{"code":0,"data":{"item":{"id_str":"article-1","modules":[]}}}`},
		{name: "authentication", body: `{"code":-101,"message":"login required"}`},
		{name: "http forbidden", code: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/frontend/finger/spi" {
					_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"test-device"}}`)
					return
				}
				switch r.URL.Path {
				case "/x/polymer/web-dynamic/v1/feed/space":
					_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, articleDynamicFixture("article-1", 1700000000))
				case "/x/polymer/web-dynamic/v1/opus/detail":
					if tt.code != 0 {
						w.WriteHeader(tt.code)
						return
					}
					_, _ = io.WriteString(w, tt.body)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			up := model.UP{UID: "42", Name: "UP", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
			require.NoError(t, store.PutUP(up))
			putServiceTestChannel(t, store)
			engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)

			require.NoError(t, engine.pollUP(t.Context(), up))
			seen, err := store.Seen("42", "article-1")
			require.NoError(t, err)
			assert.False(t, seen)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			assert.Empty(t, deliveries)
			up, err = store.UP("42")
			require.NoError(t, err)
			assert.Equal(t, 1, up.ConsecutiveFail)
		})
	}
}

func TestPollUPDoesNotFetchOpusDetailForWordDynamics(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/frontend/finger/spi" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"test-device"}}`)
			return
		}
		if r.URL.Path == "/x/polymer/web-dynamic/v1/opus/detail" {
			t.Errorf("word dynamics must not fetch opus detail")
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, dynamicFixture("word-1", 1700000000))
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
	require.NoError(t, store.PutUP(up))
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	require.NoError(t, engine.pollUP(t.Context(), up))
	seen, err := store.Seen("42", "word-1")
	require.NoError(t, err)
	assert.True(t, seen)
}

func TestPollFeedEnrichesArticleAndIsolatesDetailFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/frontend/finger/spi" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"test-device"}}`)
			return
		}
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/all/update":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"update_num":2}}`)
		case "/x/polymer/web-dynamic/v1/feed/all":
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","update_baseline":"new","update_num":2,"items":[%s,%s]}}`,
				articleDynamicWithAuthorFixture("good-article", "42", 1700000001),
				articleDynamicWithAuthorFixture("bad-article", "43", 1700000000),
			)
		case "/x/polymer/web-dynamic/v1/opus/detail":
			id := r.URL.Query().Get("id")
			if id == "bad-article" {
				_, _ = io.WriteString(w, `{"code":0,"data":{"item":{"id_str":"bad-article","modules":[]}}}`)
				return
			}
			_, _ = io.WriteString(w, articleOpusDetail(id, "综合流专栏正文", "https://i0.hdslb.com/feed.jpg"))
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
	putServiceTestChannel(t, store)

	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	client.SetSession(session)
	engine := NewEngine(store, client, testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 2), nil, nil)
	engine.setAccount(model.BiliAccount{UID: "100"})
	require.NoError(t, engine.pollFeed(t.Context(), model.BiliAccount{UID: "100"}, ups))

	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.Equal(t, "new", feed.UpdateBaseline)
	relations, err := store.FollowRelations("100")
	require.NoError(t, err)
	assert.True(t, relations["42"].SpaceSynced)
	assert.True(t, relations["43"].SpaceSynced, "durable item retries no longer disable the shared feed route")
	badUP, err := store.UP("43")
	require.NoError(t, err)
	assert.Equal(t, 1, badUP.ConsecutiveFail)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.ContentID(model.PlatformBilibili, "good-article"), deliveries[0].Dynamic.ID)
	assert.Equal(t, "综合流专栏正文", deliveries[0].Dynamic.Description)
	require.Len(t, deliveries[0].Dynamic.Media, 1)
	seen, err := store.Seen("43", "bad-article")
	require.NoError(t, err)
	assert.False(t, seen)
}

func TestPollUPEnrichesForwardedArticleOriginal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/frontend/finger/spi" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"test-device"}}`)
			return
		}
		switch r.URL.Path {
		case "/x/polymer/web-dynamic/v1/feed/space":
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, forwardedArticleFixture("fwd-1", "orig-1", 1700000001))
		case "/x/polymer/web-dynamic/v1/opus/detail":
			assert.Equal(t, "orig-1", r.URL.Query().Get("id"))
			_, _ = io.WriteString(w, articleOpusDetail("orig-1", "被转发专栏正文", "https://i0.hdslb.com/orig.jpg"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up := model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}
	require.NoError(t, store.PutUP(up))
	putServiceTestChannel(t, store)
	engine := NewEngine(store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)), testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	require.NoError(t, engine.pollUP(t.Context(), up))

	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.NotNil(t, deliveries[0].Dynamic.Original)
	assert.Equal(t, "被转发专栏正文", deliveries[0].Dynamic.Original.Description)
	require.Len(t, deliveries[0].Dynamic.Original.Media, 1)
}

func articleDynamicFixture(id string, timestamp int64) string {
	return articleDynamicWithAuthorFixture(id, "42", timestamp)
}

func articleDynamicWithAuthorFixture(id, uid string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_ARTICLE","modules":{"module_author":{"mid":%q,"name":"tester","pub_ts":%d},"module_dynamic":{"major":{"article":{"title":"专栏标题","desc":"截断摘要...","covers":["https://i0.hdslb.com/cover.jpg"],"jump_url":"https://www.bilibili.com/read/cv1"}}}}}`, id, uid, timestamp)
}

func forwardedArticleFixture(id, originalID string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_FORWARD","modules":{"module_author":{"mid":"42","name":"tester","pub_ts":%d},"module_dynamic":{"desc":{"text":"推荐专栏"},"major":null}},"orig":{"id_str":%q,"type":"DYNAMIC_TYPE_ARTICLE","modules":{"module_author":{"mid":"7","name":"author","pub_ts":%d},"module_dynamic":{"major":{"article":{"title":"原文","desc":"截断","covers":["https://i0.hdslb.com/cover.jpg"],"jump_url":"https://www.bilibili.com/read/cv2"}}}}}}`, id, timestamp, originalID, timestamp-1)
}

func articleOpusDetail(id, body, imageURL string) string {
	return fmt.Sprintf(`{"code":0,"message":"0","data":{"item":{"id_str":%q,"modules":[{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":"完整标题"}},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":%q}}]}},{"para_type":2,"pic":{"pics":[{"url":%q,"width":10,"height":20}]}}]}}]}}}`, id, body, imageURL)
}

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSocketRequiresSessionAndPublishesHTTPUpdates(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	events := service.NewEventBus()
	server := &Server{
		auth: auth,
		engine: service.NewEngine(
			store,
			bilibili.New(nil, "test"),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			nil,
			model.RuntimeSettings{
				PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 4,
				CommentEnabled: true, CommentTrackN: 10, CommentRootPages: 2,
				CommentReplyPages: 5, CommentBatchIntervalSec: 120,
			},
			events,
			nil,
		),
		store:       store,
		events:      events,
		connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	httpServer := httptest.NewTLSServer(server.adminHandler())
	t.Cleanup(httpServer.Close)
	wsURL := "wss" + strings.TrimPrefix(httpServer.URL, "https") + "/api/v1/ws"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	connection, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client()})
	if connection != nil {
		_ = connection.CloseNow()
	}
	require.Error(t, err)
	require.NotNil(t, response)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	_ = response.Body.Close()

	token, csrf, _, err := auth.createSession()
	require.NoError(t, err)
	headers := http.Header{}
	headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: token}).String())
	headers.Set("Origin", httpServer.URL)
	connection, response, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client(), HTTPHeader: headers})
	require.NoError(t, err, "authenticated dial status=%v", responseStatus(response))
	t.Cleanup(func() { _ = connection.CloseNow() })

	var initial testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &initial))
	assert.Equal(t, "snapshot", initial.Event)
	var snapshot dashboardSnapshot
	require.NoError(t, json.Unmarshal(initial.Data, &snapshot))
	assert.NotNil(t, snapshot.UPs)
	assert.NotNil(t, snapshot.Channels)
	assert.NotNil(t, snapshot.Deliveries)
	assert.NotNil(t, snapshot.MicrosoftLogins)

	badRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v1/ups", strings.NewReader(`{"uid":"42","name":"Test UP","enabled":true}`))
	require.NoError(t, err)
	badRequest.Header.Set("Content-Type", "application/json")
	badRequest.Header.Set("X-CSRF-Token", "invalid")
	badRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	badResponse, err := httpServer.Client().Do(badRequest)
	require.NoError(t, err)
	t.Cleanup(func() { _ = badResponse.Body.Close() })
	assert.Equal(t, http.StatusForbidden, badResponse.StatusCode)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v1/ups", strings.NewReader(`{"uid":"42","name":"Test UP","enabled":true}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	apiResponse, err := httpServer.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = apiResponse.Body.Close() })
	assert.Equal(t, http.StatusCreated, apiResponse.StatusCode)
	var up model.UP
	require.NoError(t, json.NewDecoder(apiResponse.Body).Decode(&up))
	assert.Equal(t, "42", up.UID)
	assert.Equal(t, "Test UP", up.Name)
	assert.True(t, up.Enabled)

	var gotUpdate bool
	for range 3 {
		var envelope testWSEnvelope
		require.NoError(t, wsjson.Read(ctx, connection, &envelope))
		if envelope.Event == "ups.updated" {
			var ups []model.UP
			require.NoError(t, json.Unmarshal(envelope.Data, &ups))
			require.Len(t, ups, 1)
			assert.Equal(t, "42", ups[0].UID)
			assert.NotZero(t, envelope.Revision)
			gotUpdate = true
		}
		if gotUpdate {
			break
		}
	}
	assert.True(t, gotUpdate, "missing ups.updated event")
}

func TestDeliveryViewsExcludeRichPayloadAndStayBounded(t *testing.T) {
	t.Parallel()
	deliveries := make([]model.Delivery, 100)
	for i := range deliveries {
		deliveries[i] = model.Delivery{
			ID: "delivery",
			Dynamic: model.Dynamic{
				ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_DRAW",
				PublishedAt: time.Now(), Summary: strings.Repeat("正文", 5000), URL: "https://t.bilibili.com/1",
				Description: strings.Repeat("不应进入管理台", 5000),
				Media:       []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://i0.hdslb.com/image.jpg"}},
				Original:    &model.Dynamic{ID: "original", Summary: strings.Repeat("原文", 5000)},
			},
			State: model.DeliveryPending,
		}
	}
	views := deliveryViews(deliveries)
	raw, err := json.Marshal(views)
	require.NoError(t, err)
	assert.Less(t, len(raw), 1<<20)
	assert.NotContains(t, string(raw), "不应进入管理台")
	assert.LessOrEqual(t, len([]rune(views[0].Dynamic.Summary)), 241)
}

func TestDynamicHistoryViewsBoundTextAndMedia(t *testing.T) {
	t.Parallel()
	media := make([]model.DynamicMedia, 0, 20)
	for i := range 20 {
		media = append(media, model.DynamicMedia{
			Kind: model.DynamicMediaImage,
			URL:  "https://i0.hdslb.com/bfs/image/" + strings.Repeat("x", 8) + string(rune('a'+i%26)) + ".jpg",
		})
	}
	view := toDynamicHistoryView(state.DynamicRecord{
		ID: "dyn", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_FORWARD",
		PublishedAt: time.Now(), DiscoveredAt: time.Now(),
		Summary: strings.Repeat("正文", 5000), Description: strings.Repeat("简介", 5000),
		Media: media,
		Stats: &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3},
		Video: &model.DynamicVideo{Duration: "01:09", Views: "8468", Danmaku: "8"},
		Original: &state.DynamicPreview{
			ID: "orig", Summary: strings.Repeat("原文", 5000), Description: strings.Repeat("原简介", 5000),
			Media: media, Video: &model.DynamicVideo{Duration: "02:00"},
		},
	})
	assert.LessOrEqual(t, len([]rune(view.Summary)), historyPreviewTextLimit+1)
	assert.LessOrEqual(t, len([]rune(view.Description)), historyPreviewTextLimit+1)
	assert.LessOrEqual(t, len(view.Media), historyPreviewMediaLimit)
	require.NotNil(t, view.Original)
	assert.LessOrEqual(t, len([]rune(view.Original.Summary)), historyPreviewTextLimit+1)
	assert.LessOrEqual(t, len(view.Original.Media), historyPreviewMediaLimit)
	require.NotNil(t, view.Stats)
	assert.Equal(t, int64(3), view.Stats.Likes)
	require.NotNil(t, view.Video)
	assert.Equal(t, "01:09", view.Video.Duration)
	require.NotNil(t, view.Original.Video)
	assert.Equal(t, "02:00", view.Original.Video.Duration)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Less(t, len(raw), 64<<10)
}

func TestAPIErrorClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		err         error
		wantCode    string
		wantMessage string
	}{
		{name: "validation", err: validationFailure(errors.New("uid is invalid")), wantCode: "validation_failed", wantMessage: "uid is invalid"},
		{name: "conflict", err: conflictFailure(errors.New("already exists")), wantCode: "conflict", wantMessage: "already exists"},
		{name: "not found", err: state.ErrNotFound, wantCode: "not_found", wantMessage: "resource not found"},
		{name: "internal", err: errors.New("database is unavailable"), wantCode: "internal", wantMessage: "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := apiError(tt.err)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantMessage, got.Message)
		})
	}
}

type testWSEnvelope struct {
	Event    string          `json:"event"`
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

func openWebTestStore(t *testing.T) *state.Store {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	require.NoError(t, err)
	store, err := state.Open(filepath.Join(t.TempDir(), "data.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func responseStatus(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}

func TestDynamicHistoryViewRewritesLocalMediaURL(t *testing.T) {
	t.Parallel()
	view := toDynamicHistoryView(state.DynamicRecord{
		ID: "10", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_DRAW",
		PublishedAt: time.Now(), DiscoveredAt: time.Now(),
		Media: []model.DynamicMedia{{
			Kind: model.DynamicMediaImage, URL: "https://i0.hdslb.com/bfs/album/a.jpg",
			LocalPath: "media/42/10/0.jpg", Width: 100, Height: 80,
		}},
	})
	require.Len(t, view.Media, 1)
	assert.Equal(t, "/api/v1/dynamics/10/media/0", view.Media[0].URL)
	assert.Empty(t, view.Media[0].LocalPath)
}

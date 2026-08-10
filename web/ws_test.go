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
	"slices"
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
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestWebSocketRequiresSessionAndPublishesHTTPUpdates(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	events := service.NewEventBus()
	settings := model.DefaultRuntimeSettings()
	engine := service.NewEngine(
		store,
		bilibili.New(nil, "test"),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service.NewMetrics(metricnoop.NewMeterProvider()),
		settings,
		events,
		nil,
	)
	settingsService := &webTestSettingsManager{engine: engine, store: store, events: events}
	server := &Server{
		auth:        auth,
		engine:      engine,
		settings:    settingsService,
		store:       store,
		events:      events,
		connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	httpServer := httptest.NewTLSServer(server.adminHandler())
	t.Cleanup(httpServer.Close)
	wsURL := "wss" + strings.TrimPrefix(httpServer.URL, "https") + "/api/v3/ws"

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
	assert.Equal(t, "sync.required", initial.Event)
	assert.Equal(t, allResourceTopics(), initial.Topics)

	badRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v3/sources", strings.NewReader(`{"platform":"bilibili","external_id":"42","name":"Test UP","enabled":true}`))
	require.NoError(t, err)
	badRequest.Header.Set("Content-Type", "application/json")
	badRequest.Header.Set("X-CSRF-Token", "invalid")
	badRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	badResponse, err := httpServer.Client().Do(badRequest)
	require.NoError(t, err)
	t.Cleanup(func() { _ = badResponse.Body.Close() })
	assert.Equal(t, http.StatusForbidden, badResponse.StatusCode)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/v3/sources", strings.NewReader(`{"platform":"bilibili","external_id":"42","name":"Test UP","enabled":true}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	apiResponse, err := httpServer.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = apiResponse.Body.Close() })
	assert.Equal(t, http.StatusCreated, apiResponse.StatusCode)
	var source model.Source
	require.NoError(t, json.NewDecoder(apiResponse.Body).Decode(&source))
	assert.Equal(t, "42", source.ExternalID)
	assert.Equal(t, "Test UP", source.Name)
	assert.True(t, source.Enabled)

	var gotUpdate bool
	for range 3 {
		var envelope testWSEnvelope
		require.NoError(t, wsjson.Read(ctx, connection, &envelope))
		if envelope.Event == "resources.invalidated" && slices.Contains(envelope.Topics, "sources") {
			assert.NotZero(t, envelope.Revision)
			gotUpdate = true
		}
		if gotUpdate {
			break
		}
	}
	assert.True(t, gotUpdate, "missing ups invalidation")
}

func TestWebSocketPublishesEveryTopicWithOneRevision(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	httpServer := httptest.NewTLSServer(fixture.server.adminHandler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	connection := dialTestWebSocket(t, ctx, httpServer, fixture.token, httpServer.URL)
	t.Cleanup(func() { _ = connection.CloseNow() })
	readSyncRequired(t, ctx, connection)

	allTopics := service.TopicStatus | service.TopicUPs | service.TopicChannels | service.TopicDeliveries |
		service.TopicBiliLogin | service.TopicMicrosoftLogin | service.TopicSettings | service.TopicDynamics |
		service.TopicComments | service.TopicAuditLogs | service.TopicAIStatus | service.TopicAIJobs |
		service.TopicAccounts | service.TopicSources | service.TopicContents | service.TopicBackfills
	revision := fixture.events.Publish(allTopics)
	var envelope testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &envelope))
	assert.Equal(t, "resources.invalidated", envelope.Event)
	assert.Equal(t, revision, envelope.Revision)
	assert.Equal(t, allResourceTopics(), envelope.Topics)
}

func TestWebSocketOriginSessionExpiryAndIdleBehavior(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *adminAPIFixture, *httptest.Server)
	}{
		{
			name: "foreign origin is rejected",
			run: func(t *testing.T, fixture *adminAPIFixture, server *httptest.Server) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				t.Cleanup(cancel)
				connection, response, err := dialTestWebSocketResponse(ctx, server, fixture.token, "https://attacker.invalid")
				if connection != nil {
					_ = connection.CloseNow()
				}
				require.Error(t, err)
				require.NotNil(t, response)
				t.Cleanup(func() { _ = response.Body.Close() })
				assert.Equal(t, http.StatusForbidden, response.StatusCode)
			},
		},
		{
			name: "idle connection emits no business event",
			run: func(t *testing.T, fixture *adminAPIFixture, server *httptest.Server) {
				fixture.server.wsHeartbeat = 10 * time.Millisecond
				fixture.server.wsPingTimeout = 100 * time.Millisecond
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				t.Cleanup(cancel)
				connection := dialTestWebSocket(t, ctx, server, fixture.token, server.URL)
				t.Cleanup(func() { _ = connection.CloseNow() })
				readSyncRequired(t, ctx, connection)

				idleCtx, idleCancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
				t.Cleanup(idleCancel)
				err := wsjson.Read(idleCtx, connection, &testWSEnvelope{})
				require.Error(t, err)
				// coder/websocket may surface either the context deadline or the
				// connection-close error caused while aborting a concurrent Ping.
				// The invariant under test is that our idle observation window
				// expired without receiving a business event.
				assert.ErrorIs(t, idleCtx.Err(), context.DeadlineExceeded)
			},
		},
		{
			name: "expired session closes promptly",
			run: func(t *testing.T, fixture *adminAPIFixture, server *httptest.Server) {
				fixture.server.wsHeartbeat = 10 * time.Millisecond
				fixture.server.wsPingTimeout = 100 * time.Millisecond
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				t.Cleanup(cancel)
				connection := dialTestWebSocket(t, ctx, server, fixture.token, server.URL)
				t.Cleanup(func() { _ = connection.CloseNow() })
				readSyncRequired(t, ctx, connection)
				fixture.server.auth.mu.Lock()
				session := fixture.server.auth.sessions[fixture.token]
				session.CreatedAt = fixture.server.auth.now().Add(-25 * time.Hour)
				fixture.server.auth.sessions[fixture.token] = session
				fixture.server.auth.mu.Unlock()
				err := wsjson.Read(ctx, connection, &testWSEnvelope{})
				require.Error(t, err)
				assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, nil)
			server := httptest.NewTLSServer(fixture.server.adminHandler())
			t.Cleanup(server.Close)
			tt.run(t, fixture, server)
		})
	}
}

func TestWebSocketIsClosedByLogoutAndPasswordChange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "logout", method: http.MethodDelete, path: "/api/v3/session"},
		{name: "password change", method: http.MethodPut, path: "/api/v3/session/password", body: `{"current_password":"correct horse battery staple","new_password":"replacement horse battery staple"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, nil)
			server := httptest.NewTLSServer(fixture.server.adminHandler())
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			t.Cleanup(cancel)
			connection := dialTestWebSocket(t, ctx, server, fixture.token, server.URL)
			t.Cleanup(func() { _ = connection.CloseNow() })
			readSyncRequired(t, ctx, connection)

			request, err := http.NewRequestWithContext(ctx, tt.method, server.URL+tt.path, strings.NewReader(tt.body))
			require.NoError(t, err)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: fixture.token})
			request.Header.Set("X-CSRF-Token", fixture.csrf)
			if tt.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })
			assert.Less(t, response.StatusCode, http.StatusBadRequest)
			for err == nil {
				err = wsjson.Read(ctx, connection, &testWSEnvelope{})
			}
			require.Error(t, err)
			assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
		})
	}
}

func TestWebSocketReconnectSyncAndDisconnectedPeerIsolation(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	server := httptest.NewTLSServer(fixture.server.adminHandler())
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	t.Cleanup(cancel)
	disconnected := dialTestWebSocket(t, ctx, server, fixture.token, server.URL)
	readSyncRequired(t, ctx, disconnected)
	_ = disconnected.CloseNow()

	require.NoError(t, fixture.store.PutUP(model.UP{UID: "42", Name: "current UP", Enabled: true}))
	fixture.events.Publish(service.TopicUPs)
	connection := dialTestWebSocket(t, ctx, server, fixture.token, server.URL)
	t.Cleanup(func() { _ = connection.CloseNow() })
	var initial testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &initial))
	assert.Equal(t, "sync.required", initial.Event)
	assert.Contains(t, initial.Topics, "sources")
	assert.Equal(t, fixture.events.Revision(), initial.Revision)

	revision := fixture.events.Publish(service.TopicUPs)
	var envelope testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &envelope))
	assert.Equal(t, "resources.invalidated", envelope.Event)
	assert.Equal(t, revision, envelope.Revision)
	assert.Equal(t, []string{"sources"}, envelope.Topics)
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

func TestDynamicHistoryViewSerializesRequiredEmptyTextFields(t *testing.T) {
	t.Parallel()
	view := toDynamicHistoryView(state.DynamicRecord{
		ID: "dyn", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD",
		PublishedAt: time.Now(), DiscoveredAt: time.Now(),
	})
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"summary":""`)
	assert.Contains(t, string(raw), `"url":""`)
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
	Event    string   `json:"event"`
	Revision uint64   `json:"revision"`
	Topics   []string `json:"topics"`
}

func dialTestWebSocket(t *testing.T, ctx context.Context, server *httptest.Server, token, origin string) *websocket.Conn {
	t.Helper()
	connection, response, err := dialTestWebSocketResponse(ctx, server, token, origin)
	require.NoError(t, err, "WebSocket dial status=%v", responseStatus(response))
	return connection
}

func dialTestWebSocketResponse(ctx context.Context, server *httptest.Server, token, origin string) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: token}).String())
	headers.Set("Origin", origin)
	wsURL := "wss" + strings.TrimPrefix(server.URL, "https") + "/api/v3/ws"
	return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: headers})
}

func readSyncRequired(t *testing.T, ctx context.Context, connection *websocket.Conn) testWSEnvelope {
	t.Helper()
	var envelope testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &envelope))
	require.Equal(t, "sync.required", envelope.Event)
	require.Equal(t, allResourceTopics(), envelope.Topics)
	return envelope
}

func openWebTestStore(t *testing.T) *state.Store {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), v)
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

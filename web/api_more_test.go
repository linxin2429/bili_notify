package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminAPILifecycle(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)

	response := fixture.request(t, http.MethodPost, "/api/v1/ups", map[string]any{
		"uid": "42", "name": "first name", "enabled": true,
	}, true)
	assert.Equal(t, http.StatusCreated, response.Code)
	var createdUP model.UP
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &createdUP))
	assert.Equal(t, "42", createdUP.UID)
	assert.Equal(t, model.CollectionRouteSpace, createdUP.CollectionRoute)

	response = fixture.request(t, http.MethodPost, "/api/v1/ups", map[string]any{
		"uid": "42", "name": "duplicate", "enabled": true,
	}, true)
	assertAPIError(t, response, http.StatusConflict, "conflict")

	response = fixture.request(t, http.MethodPut, "/api/v1/ups/42", map[string]any{
		"name": "updated name", "enabled": false,
	}, true)
	assert.Equal(t, http.StatusOK, response.Code)
	var updatedUP model.UP
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &updatedUP))
	assert.Equal(t, "updated name", updatedUP.Name)
	assert.False(t, updatedUP.Enabled)

	response = fixture.request(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "robot", "type": "wecom", "enabled": true,
		"settings": map[string]string{}, "secrets": map[string]string{"webhook": fixture.webhook.URL},
	}, true)
	assert.Equal(t, http.StatusCreated, response.Code)
	var createdChannel channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &createdChannel))
	require.NotEmpty(t, createdChannel.ID)
	assert.Equal(t, []string{"webhook"}, createdChannel.ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), fixture.webhook.URL)

	response = fixture.request(t, http.MethodPost, "/api/v1/channels/"+createdChannel.ID+"/test", nil, true)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"status":"sent"}`, response.Body.String())

	response = fixture.request(t, http.MethodPut, "/api/v1/channels/"+createdChannel.ID, map[string]any{
		"name": "renamed robot", "type": "wecom", "enabled": false,
		"settings": map[string]string{},
	}, true)
	assert.Equal(t, http.StatusOK, response.Code)
	var updatedChannel channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &updatedChannel))
	assert.Equal(t, "renamed robot", updatedChannel.Name)
	assert.False(t, updatedChannel.Enabled)
	assert.Equal(t, []string{"webhook"}, updatedChannel.ConfiguredSecrets)

	settings := webTestSettings()
	settings.PollIntervalSec = 60
	settings.RequestRate = 3
	settings.RequestConcurrency = 6
	response = fixture.request(t, http.MethodPut, "/api/v1/settings", settings, true)
	assert.Equal(t, http.StatusOK, response.Code)
	var gotSettings model.RuntimeSettings
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &gotSettings))
	assert.Equal(t, settings, gotSettings)

	response = fixture.request(t, http.MethodGet, "/api/v1/dashboard", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	var dashboard dashboardSnapshot
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &dashboard))
	assert.Len(t, dashboard.UPs, 1)
	assert.Len(t, dashboard.Channels, 1)
	assert.Equal(t, settings, dashboard.Settings)

	response = fixture.request(t, http.MethodGet, "/api/v1/audit-logs?action=channel.update&outcome=success&limit=100", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	var auditPage struct {
		Items []state.AuditLog `json:"items"`
		Total int              `json:"total"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &auditPage))
	assert.Equal(t, 1, auditPage.Total)
	require.Len(t, auditPage.Items, 1)
	assert.Equal(t, "channel.update", auditPage.Items[0].Action)

	response = fixture.request(t, http.MethodDelete, "/api/v1/channels/"+createdChannel.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)
	response = fixture.request(t, http.MethodDelete, "/api/v1/ups/42", nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Greater(t, fixture.events.Revision(), uint64(0))
}

func TestContentAPIs(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	published := time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC)
	require.NoError(t, fixture.store.PutUP(model.UP{UID: "42", Name: "UP", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	channel, err := fixture.store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": fixture.webhook.URL},
	})
	require.NoError(t, err)
	relativeMedia := filepath.Join("media", "42", "dynamic-1", "0.png")
	absMedia := filepath.Join(fixture.dataDir, relativeMedia)
	require.NoError(t, os.MkdirAll(filepath.Dir(absMedia), 0o700))
	require.NoError(t, os.WriteFile(absMedia, []byte("\x89PNG\r\n\x1a\ncontract"), 0o600))
	dynamic := model.Dynamic{
		ID: "dynamic-1", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_DRAW",
		PublishedAt: published, Title: "title", Summary: "summary", URL: "https://t.bilibili.com/1",
		Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.invalid/image.png", LocalPath: relativeMedia, ContentType: "image/png", Width: 10, Height: 20}},
		Stats: &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3},
		Video: &model.DynamicVideo{Duration: "01:00", Views: "10", Danmaku: "2"},
	}
	created, err := fixture.store.RecordDynamics("42", []model.Dynamic{dynamic}, []string{channel.ID}, state.DynamicBaselineNone)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	target := model.CommentTarget{
		UID: "42", UPName: "UP", DynamicID: dynamic.ID, ContentType: dynamic.Type,
		Title: dynamic.Title, URL: dynamic.URL, CommentType: 11, CommentOID: "oid-1", PublishedAt: published,
	}
	require.NoError(t, fixture.store.PutCommentTargets("42", []model.CommentTarget{target}))
	note := model.CommentNotification{
		RPID: "reply-1", UPUID: "42", UPName: "UP", ContentType: dynamic.Type,
		ContentID: dynamic.ID, ContentTitle: dynamic.Title, ContentURL: dynamic.URL, PublishedAt: published,
		Thread: []model.CommentNode{{RPID: "root", Mid: "7", Name: "viewer", Message: "hello", Time: published}, {RPID: "reply-1", Parent: "root", Mid: "42", Name: "UP", Message: "reply", Time: published, IsUP: true, IsTrigger: true}},
	}
	_, err = fixture.store.RecordCommentNotifications(target, []model.CommentNotification{note}, []string{channel.ID}, false)
	require.NoError(t, err)

	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantContain string
		contentType string
	}{
		{name: "dynamic list", path: "/api/v1/dynamics?uid=42&q=summary&limit=1&offset=0", wantStatus: http.StatusOK, wantContain: `"dynamic-1"`},
		{name: "dynamic detail", path: "/api/v1/dynamics/dynamic-1", wantStatus: http.StatusOK, wantContain: `"summary"`},
		{name: "dynamic media", path: "/api/v1/dynamics/dynamic-1/media/0", wantStatus: http.StatusOK, wantContain: "PNG", contentType: "image/png"},
		{name: "comment list", path: "/api/v1/comments?uid=42&q=reply", wantStatus: http.StatusOK, wantContain: `"reply-1"`},
		{name: "comment detail", path: "/api/v1/comments/reply-1", wantStatus: http.StatusOK, wantContain: `"thread"`},
		{name: "missing dynamic", path: "/api/v1/dynamics/missing", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
		{name: "missing comment", path: "/api/v1/comments/missing", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
		{name: "invalid media index", path: "/api/v1/dynamics/dynamic-1/media/nope", wantStatus: http.StatusBadRequest, wantContain: `"validation_error"`},
		{name: "missing media index", path: "/api/v1/dynamics/dynamic-1/media/9", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := fixture.request(t, http.MethodGet, tt.path, nil, false)
			assert.Equal(t, tt.wantStatus, response.Code)
			assert.Contains(t, response.Body.String(), tt.wantContain)
			if tt.contentType != "" {
				assert.Equal(t, tt.contentType, response.Header().Get("Content-Type"))
				assert.Equal(t, "private, max-age=86400", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAdminAPIRequestValidation(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	invalidSettings := webTestSettings()
	invalidSettings.PollIntervalSec = 1
	invalidSettingsJSON, err := json.Marshal(invalidSettings)
	require.NoError(t, err)
	validSettingsJSON, err := json.Marshal(webTestSettings())
	require.NoError(t, err)
	settingsObject := make(map[string]any)
	require.NoError(t, json.Unmarshal(validSettingsJSON, &settingsObject))
	settingsObject["delivery_retry_delays_sec"] = []int{5}
	shortRetryJSON, err := json.Marshal(settingsObject)
	require.NoError(t, err)
	settingsObject["delivery_retry_delays_sec"] = []int{5, 30, 120, 600, 3600}
	settingsObject["unknown"] = true
	unknownSettingsJSON, err := json.Marshal(settingsObject)
	require.NoError(t, err)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{name: "invalid json", method: http.MethodPost, path: "/api/v1/ups", body: `{`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown field", method: http.MethodPost, path: "/api/v1/ups", body: `{"uid":"42","name":"up","enabled":true,"unknown":1}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "multiple values", method: http.MethodPost, path: "/api/v1/ups", body: `{} {}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid up", method: http.MethodPost, path: "/api/v1/ups", body: `{"uid":"","name":"up","enabled":true}`, status: http.StatusBadRequest, code: "validation_failed"},
		{name: "missing settings", method: http.MethodPut, path: "/api/v1/settings", body: `{"poll_interval_sec":30}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "short retry settings", method: http.MethodPut, path: "/api/v1/settings", body: string(shortRetryJSON), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown setting", method: http.MethodPut, path: "/api/v1/settings", body: string(unknownSettingsJSON), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid settings", method: http.MethodPut, path: "/api/v1/settings", body: string(invalidSettingsJSON), status: http.StatusBadRequest, code: "validation_failed"},
		{name: "invalid audit outcome", method: http.MethodGet, path: "/api/v1/audit-logs?outcome=maybe", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit from", method: http.MethodGet, path: "/api/v1/audit-logs?from=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit to", method: http.MethodGet, path: "/api/v1/audit-logs?to=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "reversed audit range", method: http.MethodGet, path: "/api/v1/audit-logs?from=2026-08-06T02:00:00Z&to=2026-08-06T01:00:00Z", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit limit", method: http.MethodGet, path: "/api/v1/audit-logs?limit=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit offset", method: http.MethodGet, path: "/api/v1/audit-logs?offset=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content limit", method: http.MethodGet, path: "/api/v1/dynamics?limit=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content offset", method: http.MethodGet, path: "/api/v1/comments?offset=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content range", method: http.MethodGet, path: "/api/v1/dynamics?from=2026-08-06T02:00:00Z&to=2026-08-06T01:00:00Z", status: http.StatusBadRequest, code: "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var body any
			if tt.body != "" {
				body = json.RawMessage(tt.body)
			}
			response := fixture.request(t, tt.method, tt.path, body, tt.method != http.MethodGet)
			assertAPIError(t, response, tt.status, tt.code)
		})
	}
}

func TestAdminAPIWriteAuthorization(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create up", method: http.MethodPost, path: "/api/v1/ups"},
		{name: "update up", method: http.MethodPut, path: "/api/v1/ups/42"},
		{name: "delete up", method: http.MethodDelete, path: "/api/v1/ups/42"},
		{name: "create channel", method: http.MethodPost, path: "/api/v1/channels"},
		{name: "update channel", method: http.MethodPut, path: "/api/v1/channels/id"},
		{name: "delete channel", method: http.MethodDelete, path: "/api/v1/channels/id"},
		{name: "test channel", method: http.MethodPost, path: "/api/v1/channels/id/test"},
		{name: "retry delivery", method: http.MethodPost, path: "/api/v1/deliveries/id/retry"},
		{name: "start bili login", method: http.MethodPost, path: "/api/v1/bilibili-login"},
		{name: "cancel bili login", method: http.MethodDelete, path: "/api/v1/bilibili-login/id"},
		{name: "start microsoft login", method: http.MethodPost, path: "/api/v1/channels/id/microsoft-login"},
		{name: "cancel microsoft login", method: http.MethodDelete, path: "/api/v1/channels/id/microsoft-login"},
		{name: "update settings", method: http.MethodPut, path: "/api/v1/settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			unauthenticated := httptest.NewRequest(tt.method, tt.path, nil)
			unauthenticated.RemoteAddr = "192.0.2.10:1234"
			unauthenticatedResponse := httptest.NewRecorder()
			fixture.server.adminHandler().ServeHTTP(unauthenticatedResponse, unauthenticated)
			assertAPIError(t, unauthenticatedResponse, http.StatusUnauthorized, "authentication_required")

			invalidCSRF := httptest.NewRequest(tt.method, tt.path, nil)
			invalidCSRF.RemoteAddr = "192.0.2.11:1234"
			invalidCSRF.AddCookie(&http.Cookie{Name: sessionCookie, Value: fixture.token})
			invalidCSRF.Header.Set("X-CSRF-Token", "wrong")
			invalidCSRFResponse := httptest.NewRecorder()
			fixture.server.adminHandler().ServeHTTP(invalidCSRFResponse, invalidCSRF)
			assertAPIError(t, invalidCSRFResponse, http.StatusForbidden, "invalid_csrf")
		})
	}
}

type adminAPIFixture struct {
	server  *Server
	store   *state.Store
	events  *service.EventBus
	token   string
	csrf    string
	dataDir string
	webhook *httptest.Server
}

type webTestSettingsManager struct {
	engine *service.Engine
	store  *state.Store
	events *service.EventBus
}

func (m *webTestSettingsManager) Settings() model.RuntimeSettings { return m.engine.Settings() }

func (m *webTestSettingsManager) UpdateSettings(settings model.RuntimeSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	if err := m.store.PutRuntimeSettings(settings); err != nil {
		return err
	}
	m.engine.ApplySettings(settings)
	m.events.Publish(service.TopicSettings | service.TopicStatus)
	return nil
}

func newAdminAPIFixture(t *testing.T, client *http.Client) *adminAPIFixture {
	t.Helper()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	token, csrf, _, err := auth.createSession()
	require.NoError(t, err)
	webhook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 0, "errmsg": "ok"})
	}))
	t.Cleanup(webhook.Close)
	if client == nil {
		client = webhook.Client()
	}
	events := service.NewEventBus()
	registry := prometheus.NewRegistry()
	metrics := service.NewMetrics(registry)
	engine := service.NewEngine(
		store, bilibili.New(client, "web-api-test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics, webTestSettings(), events, nil, service.WithNotificationHTTPClient(client),
	)
	settingsManager := &webTestSettingsManager{engine: engine, store: store, events: events}
	dataDir := t.TempDir()
	server := &Server{
		auth: auth, engine: engine, settings: settingsManager, store: store, events: events,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: metrics, registry: registry,
		dataDir: dataDir, connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	return &adminAPIFixture{server: server, store: store, events: events, token: token, csrf: csrf, dataDir: dataDir, webhook: webhook}
}

func (f *adminAPIFixture) request(t *testing.T, method, path string, body any, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			encoded = raw
		} else {
			var err error
			encoded, err = json.Marshal(body)
			require.NoError(t, err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.RemoteAddr = "192.0.2.20:1234"
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.token})
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", f.csrf)
	}
	response := httptest.NewRecorder()
	f.server.adminHandler().ServeHTTP(response, request)
	assert.NotEmpty(t, response.Header().Get("X-Request-ID"))
	return response
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assert.Equal(t, status, response.Code)
	var body struct {
		Error wsAPIError `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, code, body.Error.Code)
}

func webTestSettings() model.RuntimeSettings {
	settings := model.DefaultRuntimeSettings()
	settings.RequestRate = 10
	return settings
}

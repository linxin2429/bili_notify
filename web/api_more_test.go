package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/sources"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/zsxq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAdminAPILifecycle(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)

	response := fixture.request(t, http.MethodPost, "/api/v4/sources/bilibili", map[string]any{
		"uid": "42", "name": "first name", "note": "", "enabled": true,
	}, true)
	assert.Equal(t, http.StatusCreated, response.Code)
	var createdSource model.Source
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &createdSource))
	assert.Equal(t, "42", createdSource.ExternalID)
	assert.Equal(t, model.SourceBilibiliUP, createdSource.Type)

	response = fixture.request(t, http.MethodPost, "/api/v4/sources/bilibili", map[string]any{
		"uid": "42", "name": "duplicate", "note": "", "enabled": true,
	}, true)
	assertAPIError(t, response, http.StatusConflict, "conflict")

	planet := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "9", Name: "Planet", Enabled: false}
	require.NoError(t, fixture.store.CreateSource(planet))

	response = fixture.request(t, http.MethodPut, "/api/v4/sources/bilibili:up:42", map[string]any{
		"name": "updated name", "note": "note", "enabled": false,
	}, true)
	assert.Equal(t, http.StatusOK, response.Code)
	var updatedSource model.Source
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &updatedSource))
	assert.Equal(t, "updated name", updatedSource.Name)
	assert.False(t, updatedSource.Enabled)

	response = fixture.request(t, http.MethodPost, "/api/v4/channels", map[string]any{
		"name": "robot", "type": "wecom", "enabled": true,
		"settings": map[string]string{}, "secrets": map[string]string{"webhook": fixture.webhook.URL},
	}, true)
	assert.Equal(t, http.StatusCreated, response.Code)
	var createdChannel channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &createdChannel))
	require.NotEmpty(t, createdChannel.ID)
	assert.Equal(t, []string{"webhook"}, createdChannel.ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), fixture.webhook.URL)

	response = fixture.request(t, http.MethodPost, "/api/v4/channels/"+createdChannel.ID+"/test", nil, true)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"status":"sent"}`, response.Body.String())

	response = fixture.request(t, http.MethodPut, "/api/v4/channels/"+createdChannel.ID, map[string]any{
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
	settings.BilibiliDynamicIntervalSec = 60
	settings.BilibiliRequestRate = 3
	settings.BilibiliRequestConcurrency = 6
	response = fixture.request(t, http.MethodPut, "/api/v4/settings", settings, true)
	assert.Equal(t, http.StatusOK, response.Code)
	var gotSettings model.RuntimeSettings
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &gotSettings))
	assert.Equal(t, settings, gotSettings)

	response = fixture.request(t, http.MethodGet, "/api/v4/sources?platform=bilibili", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	var sources []model.Source
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &sources))
	assert.Len(t, sources, 1)
	response = fixture.request(t, http.MethodGet, "/api/v4/channels", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	var channels []channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &channels))
	assert.Len(t, channels, 1)
	response = fixture.request(t, http.MethodGet, "/api/v4/settings", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &gotSettings))
	assert.Equal(t, settings, gotSettings)

	response = fixture.request(t, http.MethodGet, "/api/v4/audit-logs?action=channel.update&outcome=success&limit=100", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	var auditPage struct {
		Items []state.AuditLog `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &auditPage))
	require.Len(t, auditPage.Items, 1)
	assert.Equal(t, "channel.update", auditPage.Items[0].Action)

	response = fixture.request(t, http.MethodDelete, "/api/v4/channels/"+createdChannel.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)
	response = fixture.request(t, http.MethodDelete, "/api/v4/sources/bilibili:up:42", nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)
	response = fixture.request(t, http.MethodDelete, "/api/v4/sources/zsxq:planet:9", nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Greater(t, fixture.events.Revision(), uint64(0))
}

func TestZSXQGroupDiscoveryAndSourceCreation(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/groups", r.URL.Path)
		assert.Equal(t, "session-secret", r.Header.Get("Authorization"))
		writeJSON(w, http.StatusOK, map[string]any{"succeeded": true, "code": 0, "resp_data": map[string]any{"groups": []map[string]any{
			{"group_id": 9, "name": "账号星球", "owner": map[string]any{"user_id": 8, "name": "星主"}},
		}}})
	}))
	t.Cleanup(upstream.Close)
	fixture := newAdminAPIFixture(t, nil)
	require.NoError(t, fixture.store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "7", DisplayName: "成员", Status: model.AccountConnected, Session: map[string]string{zsxq.AccessTokenKey: "session-secret"}}))
	client, err := zsxq.New(upstream.Client(), "web-zsxq-test", zsxq.WithBaseURL(upstream.URL))
	require.NoError(t, err)
	manager, err := zsxq.NewAccountManager(client, fixture.store)
	require.NoError(t, err)
	fixture.server.zsxqAccounts = manager

	response := fixture.request(t, http.MethodGet, "/api/v4/accounts/zsxq/groups", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `[{"id":"9","name":"账号星球","owner_id":"8","owner_name":"星主"}]`, response.Body.String())
	assert.NotContains(t, response.Body.String(), "session-secret")

	response = fixture.request(t, http.MethodPost, "/api/v4/sources/zsxq", map[string]any{"group_id": "9", "note": "重点", "enabled": true}, true)
	assert.Equal(t, http.StatusCreated, response.Code)
	var source model.Source
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &source))
	assert.Equal(t, "账号星球", source.Name)
	assert.Equal(t, "8", source.OwnerID)
	assert.Equal(t, "星主", source.OwnerName)
	assert.Equal(t, "重点", source.Note)

	response = fixture.request(t, http.MethodPost, "/api/v4/sources/zsxq", map[string]any{"group_id": "10", "note": "", "enabled": true}, true)
	assertAPIError(t, response, http.StatusUnprocessableEntity, "validation_failed")
	response = fixture.request(t, http.MethodPost, "/api/v4/sources", map[string]any{}, true)
	assert.Equal(t, http.StatusMethodNotAllowed, response.Code)

	disconnected := newAdminAPIFixture(t, nil)
	disconnectedManager, err := zsxq.NewAccountManager(client, disconnected.store)
	require.NoError(t, err)
	disconnected.server.zsxqAccounts = disconnectedManager
	response = disconnected.request(t, http.MethodGet, "/api/v4/accounts/zsxq/groups", nil, false)
	assertAPIError(t, response, http.StatusConflict, "account_not_connected")
}

func TestBilibiliLoginHTTPAPIAndAuditLifecycle(t *testing.T) {
	t.Parallel()
	var generated int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/passport-login/web/qrcode/generate" {
			http.NotFound(w, r)
			return
		}
		generated++
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]string{
			"url": "https://example.invalid/login?secret=raw-qr-value", "qrcode_key": "login-" + strconv.Itoa(generated),
		}})
	}))
	t.Cleanup(upstream.Close)
	fixture := newAdminAPIFixtureWithBilibili(t, upstream.Client(), upstream.URL)
	stopEngine := runWebTestEngine(t, fixture.engine)
	t.Cleanup(stopEngine)

	first := fixture.request(t, http.MethodPost, "/api/v4/accounts/bilibili/qr", nil, true)
	assert.Equal(t, http.StatusCreated, first.Code)
	assert.NotContains(t, first.Body.String(), "raw-qr-value")
	var firstLogin biliLoginView
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstLogin))
	assert.Equal(t, "login-1", firstLogin.ID)
	assert.True(t, strings.HasPrefix(firstLogin.QRDataURL, "data:image/png;base64,"))

	second := fixture.request(t, http.MethodPost, "/api/v4/accounts/bilibili/qr", nil, true)
	assert.Equal(t, http.StatusCreated, second.Code)
	var secondLogin biliLoginView
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondLogin))
	assert.Equal(t, "login-2", secondLogin.ID)
	_, err := fixture.engine.LoginURL(firstLogin.ID)
	require.Error(t, err)

	canceled := fixture.request(t, http.MethodDelete, "/api/v4/accounts/bilibili/qr/"+secondLogin.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, canceled.Code)
	_, ok := fixture.engine.Login()
	assert.False(t, ok)

	for _, expected := range []struct {
		action     string
		resourceID string
		count      int
	}{
		{action: "bilibili.login.start", resourceID: secondLogin.ID, count: 2},
		{action: "bilibili.login.cancel", resourceID: secondLogin.ID, count: 1},
	} {
		entries, total, queryErr := fixture.store.QueryAuditLogs(state.AuditQuery{Action: expected.action, Outcome: state.AuditSuccess})
		require.NoError(t, queryErr)
		assert.Equal(t, expected.count, total)
		require.NotEmpty(t, entries)
		assert.Equal(t, expected.resourceID, entries[0].ResourceID)
		assert.Equal(t, "administrator", entries[0].Actor)
	}
}

func TestBilibiliLoginHTTPUpstreamFailuresAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		client  func(*httptest.Server) *http.Client
	}{
		{
			name: "upstream error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream-secret-body", http.StatusInternalServerError)
			},
			client: func(server *httptest.Server) *http.Client { return server.Client() },
		},
		{
			name: "upstream timeout",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
				case <-timer.C:
				}
			},
			client: func(server *httptest.Server) *http.Client {
				client := server.Client()
				client.Timeout = 25 * time.Millisecond
				return client
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(tt.handler)
			t.Cleanup(upstream.Close)
			fixture := newAdminAPIFixtureWithBilibili(t, tt.client(upstream), upstream.URL)
			stopEngine := runWebTestEngine(t, fixture.engine)
			t.Cleanup(stopEngine)
			response := fixture.request(t, http.MethodPost, "/api/v4/accounts/bilibili/qr", nil, true)
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			assert.NotContains(t, response.Body.String(), "upstream-secret-body")
			assert.NotContains(t, response.Body.String(), upstream.URL)
			entries, total, err := fixture.store.QueryAuditLogs(state.AuditQuery{Action: "bilibili.login.start"})
			require.NoError(t, err)
			assert.Equal(t, 1, total)
			require.Len(t, entries, 1)
			assert.Equal(t, state.AuditFailure, entries[0].Outcome)
			assert.Equal(t, "internal", entries[0].ErrorCode)
		})
	}
}

func TestMicrosoftLoginHTTPAPIAndAuditLifecycle(t *testing.T) {
	t.Parallel()
	deviceRequests := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/devicecode"):
			deviceRequests <- struct{}{}
			writeJSON(w, http.StatusOK, map[string]any{
				"device_code": "device-secret", "user_code": "ABCD-EFGH",
				"verification_uri": "https://microsoft.example/device", "expires_in": 900, "interval": 60,
			})
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	client := rewriteHTTPClient(upstream)
	fixture := newAdminAPIFixture(t, client)
	channel, err := fixture.store.PutChannel(model.Channel{
		Name: "Microsoft", Type: model.ChannelMicrosoft, Enabled: false,
		Settings: map[string]string{"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common", "to": "admin@example.com"},
	})
	require.NoError(t, err)
	stopEngine := runWebTestEngine(t, fixture.engine)
	t.Cleanup(stopEngine)

	for range 2 {
		response := fixture.request(t, http.MethodPost, "/api/v4/channels/"+channel.ID+"/microsoft-login", nil, true)
		assert.Equal(t, http.StatusCreated, response.Code)
		assert.Contains(t, response.Body.String(), "ABCD-EFGH")
		assert.NotContains(t, response.Body.String(), "device-secret")
		select {
		case <-deviceRequests:
		case <-time.After(time.Second):
			require.FailNow(t, "Microsoft device request was not observed")
		}
	}
	canceled := fixture.request(t, http.MethodDelete, "/api/v4/channels/"+channel.ID+"/microsoft-login", nil, true)
	assert.Equal(t, http.StatusNoContent, canceled.Code)
	login, err := fixture.engine.MicrosoftLogin(channel.ID)
	require.NoError(t, err)
	assert.Equal(t, "canceled", login.Status)

	for _, expected := range []struct {
		action string
		count  int
	}{{action: "microsoft.login.start", count: 2}, {action: "microsoft.login.cancel", count: 1}} {
		entries, total, queryErr := fixture.store.QueryAuditLogs(state.AuditQuery{Action: expected.action, Outcome: state.AuditSuccess})
		require.NoError(t, queryErr)
		assert.Equal(t, expected.count, total)
		require.NotEmpty(t, entries)
		assert.Equal(t, channel.ID, entries[0].ResourceID)
	}
}

func TestMicrosoftLoginHTTPUpstreamFailuresAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
	}{
		{
			name: "identity provider error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "identity-secret-body", http.StatusInternalServerError)
			},
		},
		{
			name: "identity provider timeout",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
				case <-timer.C:
				}
			},
			timeout: 25 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(tt.handler)
			t.Cleanup(upstream.Close)
			client := rewriteHTTPClient(upstream)
			client.Timeout = tt.timeout
			fixture := newAdminAPIFixture(t, client)
			channel, err := fixture.store.PutChannel(model.Channel{
				Name: "Microsoft", Type: model.ChannelMicrosoft,
				Settings: map[string]string{"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common", "to": "admin@example.com"},
			})
			require.NoError(t, err)
			stopEngine := runWebTestEngine(t, fixture.engine)
			t.Cleanup(stopEngine)
			response := fixture.request(t, http.MethodPost, "/api/v4/channels/"+channel.ID+"/microsoft-login", nil, true)
			assert.Equal(t, http.StatusInternalServerError, response.Code)
			assert.NotContains(t, response.Body.String(), "identity-secret-body")
			assert.NotContains(t, response.Body.String(), upstream.URL)
			entries, total, queryErr := fixture.store.QueryAuditLogs(state.AuditQuery{Action: "microsoft.login.start"})
			require.NoError(t, queryErr)
			assert.Equal(t, 1, total)
			require.Len(t, entries, 1)
			assert.Equal(t, channel.ID, entries[0].ResourceID)
			assert.Equal(t, state.AuditFailure, entries[0].Outcome)
		})
	}
}

func TestChannelTestHTTPFailureTimeoutAndAuditRedaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		client  func(*httptest.Server) *http.Client
	}{
		{
			name: "channel rejects request",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"errcode": 40001, "errmsg": "webhook-secret-detail"})
			},
			client: func(server *httptest.Server) *http.Client { return server.Client() },
		},
		{
			name: "channel times out",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
				case <-timer.C:
				}
			},
			client: func(server *httptest.Server) *http.Client {
				client := server.Client()
				client.Timeout = 25 * time.Millisecond
				return client
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewTLSServer(tt.handler)
			t.Cleanup(upstream.Close)
			fixture := newAdminAPIFixture(t, tt.client(upstream))
			channel, err := fixture.store.PutChannel(model.Channel{
				Name: "robot", Type: model.ChannelWeCom, Enabled: true,
				Settings: map[string]string{"webhook": upstream.URL + "/send?key=webhook-secret-query"},
			})
			require.NoError(t, err)
			response := fixture.request(t, http.MethodPost, "/api/v4/channels/"+channel.ID+"/test", nil, true)
			assert.Equal(t, http.StatusBadGateway, response.Code)
			assert.NotContains(t, response.Body.String(), "webhook-secret")
			assert.NotContains(t, response.Body.String(), upstream.URL)
			entries, total, queryErr := fixture.store.QueryAuditLogs(state.AuditQuery{Action: "channel.test"})
			require.NoError(t, queryErr)
			assert.Equal(t, 1, total)
			require.Len(t, entries, 1)
			assert.Equal(t, channel.ID, entries[0].ResourceID)
			assert.Equal(t, state.AuditFailure, entries[0].Outcome)
			assert.Equal(t, "upstream_failure", entries[0].ErrorCode)
		})
	}
}

func TestContentAPIs(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	published := time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC)
	source := model.Source{ID: "bilibili:up:42", Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP,
		ExternalID: "42", Name: "UP", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, fixture.store.PutSource(source))
	contentID := model.ContentID(model.PlatformBilibili, "dynamic-1")
	relativeMedia := filepath.Join("media", "bilibili", "source", "content", "image.png")
	absMedia := filepath.Join(fixture.dataDir, relativeMedia)
	require.NoError(t, os.MkdirAll(filepath.Dir(absMedia), 0o700))
	require.NoError(t, os.WriteFile(absMedia, []byte("\x89PNG\r\n\x1a\ncontract"), 0o600))
	content := model.Content{
		ID: contentID, Platform: model.PlatformBilibili, SourceID: source.ID, ExternalID: "dynamic-1", AuthorID: "42", AuthorName: "UP",
		UpstreamType: "DYNAMIC_TYPE_DRAW", Type: model.ContentDynamic, PublishedAt: published, FirstSeenAt: published, LastSyncedAt: published,
		Title: "title", Text: "summary", URL: "https://t.bilibili.com/1", Stats: map[string]int64{"comments": 2},
	}
	attachment := model.Attachment{ID: "bilibili:attachment:image-1", ContentID: contentID, ExternalID: "image-1", Type: model.AttachmentImage,
		FileName: "image.png", MIME: "image/png", Size: 16, Width: 10, Height: 20, LocalPath: relativeMedia}
	require.NoError(t, fixture.store.ArchiveContent(content, []model.Attachment{attachment}))
	nodes := []model.CommentNode{
		{ID: "bilibili:comment:root", Platform: model.PlatformBilibili, ContentID: contentID, RootID: "bilibili:comment:root", RPID: "root", Mid: "7", Name: "viewer", Message: "hello", Time: published, Role: model.RoleMember},
		{ID: "bilibili:comment:reply-1", Platform: model.PlatformBilibili, ContentID: contentID, RootID: "bilibili:comment:root", ParentID: "bilibili:comment:root", RPID: "reply-1", Mid: "42", Name: "UP", Message: "reply", Time: published, Role: model.RoleUP},
	}
	_, err := fixture.store.SyncCommentTree(content, nodes, true, true, "baseline", nil)
	require.NoError(t, err)

	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantContain string
		contentType string
	}{
		{name: "content list", path: "/api/v4/contents?source_id=bilibili:up:42&q=summary&limit=1", wantStatus: http.StatusOK, wantContain: `"bilibili:content:dynamic-1"`},
		{name: "content detail", path: "/api/v4/contents/" + contentID, wantStatus: http.StatusOK, wantContain: `"summary"`},
		{name: "attachment", path: "/api/v4/contents/" + contentID + "/attachments/" + attachment.ID, wantStatus: http.StatusOK, wantContain: "PNG", contentType: "image/png"},
		{name: "comment tree", path: "/api/v4/contents/" + contentID + "/comments", wantStatus: http.StatusOK, wantContain: `"children"`},
		{name: "missing content", path: "/api/v4/contents/missing", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
		{name: "missing comment tree", path: "/api/v4/contents/missing/comments", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
		{name: "missing attachment", path: "/api/v4/contents/" + contentID + "/attachments/missing", wantStatus: http.StatusNotFound, wantContain: `"not_found"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := fixture.request(t, http.MethodGet, tt.path, nil, false)
			assert.Equal(t, tt.wantStatus, response.Code)
			assert.Contains(t, response.Body.String(), tt.wantContain)
			if tt.contentType != "" {
				assert.Equal(t, tt.contentType, response.Header().Get("Content-Type"))
				assert.Contains(t, response.Header().Get("Content-Disposition"), "attachment")
				assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
			}
		})
	}
}

func TestAdminAPIRequestValidation(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	invalidSettings := webTestSettings()
	invalidSettings.BilibiliDynamicIntervalSec = 1
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
		{name: "invalid json", method: http.MethodPost, path: "/api/v4/sources/bilibili", body: `{`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown field", method: http.MethodPost, path: "/api/v4/sources/bilibili", body: `{"uid":"42","name":"up","note":"","enabled":true,"unknown":1}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "channel create id belongs to server", method: http.MethodPost, path: "/api/v4/channels", body: `{"id":"client-id","name":"mail","type":"email","enabled":true,"settings":{}}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "channel update id belongs to path", method: http.MethodPut, path: "/api/v4/channels/server-id", body: `{"id":"client-id","name":"mail","type":"email","enabled":true,"settings":{}}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "multiple values", method: http.MethodPost, path: "/api/v4/sources/bilibili", body: `{} {}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid source", method: http.MethodPost, path: "/api/v4/sources/bilibili", body: `{"uid":"","name":"up","note":"","enabled":true}`, status: http.StatusBadRequest, code: "validation_failed"},
		{name: "missing settings", method: http.MethodPut, path: "/api/v4/settings", body: `{"poll_interval_sec":30}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "short retry settings", method: http.MethodPut, path: "/api/v4/settings", body: string(shortRetryJSON), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown setting", method: http.MethodPut, path: "/api/v4/settings", body: string(unknownSettingsJSON), status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid settings", method: http.MethodPut, path: "/api/v4/settings", body: string(invalidSettingsJSON), status: http.StatusBadRequest, code: "validation_failed"},
		{name: "invalid audit outcome", method: http.MethodGet, path: "/api/v4/audit-logs?outcome=maybe", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit from", method: http.MethodGet, path: "/api/v4/audit-logs?from=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit to", method: http.MethodGet, path: "/api/v4/audit-logs?to=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "reversed audit range", method: http.MethodGet, path: "/api/v4/audit-logs?from=2026-08-06T02:00:00Z&to=2026-08-06T01:00:00Z", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit limit", method: http.MethodGet, path: "/api/v4/audit-logs?limit=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid audit cursor", method: http.MethodGet, path: "/api/v4/audit-logs?after=not-base64", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content limit", method: http.MethodGet, path: "/api/v4/contents?limit=bad", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content cursor", method: http.MethodGet, path: "/api/v4/contents?after=not-base64", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "invalid content range", method: http.MethodGet, path: "/api/v4/contents?from=2026-08-06T02:00:00Z&to=2026-08-06T01:00:00Z", status: http.StatusBadRequest, code: "validation_failed"},
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
		{name: "create source", method: http.MethodPost, path: "/api/v4/sources/bilibili"},
		{name: "update source", method: http.MethodPut, path: "/api/v4/sources/bilibili:up:42"},
		{name: "delete source", method: http.MethodDelete, path: "/api/v4/sources/bilibili:up:42"},
		{name: "create channel", method: http.MethodPost, path: "/api/v4/channels"},
		{name: "update channel", method: http.MethodPut, path: "/api/v4/channels/id"},
		{name: "delete channel", method: http.MethodDelete, path: "/api/v4/channels/id"},
		{name: "test channel", method: http.MethodPost, path: "/api/v4/channels/id/test"},
		{name: "retry delivery", method: http.MethodPost, path: "/api/v4/deliveries/id/retry"},
		{name: "start bili login", method: http.MethodPost, path: "/api/v4/accounts/bilibili/qr"},
		{name: "cancel bili login", method: http.MethodDelete, path: "/api/v4/accounts/bilibili/qr/id"},
		{name: "start microsoft login", method: http.MethodPost, path: "/api/v4/channels/id/microsoft-login"},
		{name: "cancel microsoft login", method: http.MethodDelete, path: "/api/v4/channels/id/microsoft-login"},
		{name: "update settings", method: http.MethodPut, path: "/api/v4/settings"},
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
	engine  *service.Engine
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
	meterProvider := metricnoop.NewMeterProvider()
	metrics := service.NewMetrics(meterProvider)
	engine := service.NewEngine(
		store, bilibili.New(client, "web-api-test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics, webTestSettings(), events, nil, service.WithNotificationHTTPClient(client),
	)
	settingsManager := &webTestSettingsManager{engine: engine, store: store, events: events}
	dataDir := t.TempDir()
	server := &Server{
		auth: auth, engine: engine, settings: settingsManager, store: store, events: events,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: metrics,
		dataDir: dataDir, connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	server.sourceAdmin = newWebTestSourceAdmin(t, server, store, events)
	return &adminAPIFixture{server: server, engine: engine, store: store, events: events, token: token, csrf: csrf, dataDir: dataDir, webhook: webhook}
}

func newWebTestSourceAdmin(t *testing.T, server *Server, store *state.Store, events *service.EventBus) *sources.Admin {
	t.Helper()
	admin, err := sources.NewAdmin(
		func(ctx context.Context) sources.Repository { return store.WithContext(ctx) },
		func() { server.engine.NotifyUPChanged() },
		func() {
			events.Publish(service.TopicStatus | service.TopicUPs | service.TopicSources | service.TopicBackfills)
		},
		func() { events.Publish(service.TopicSources | service.TopicContents | service.TopicBackfills) },
	)
	require.NoError(t, err)
	return admin
}

func newAdminAPIFixtureWithBilibili(t *testing.T, client *http.Client, baseURL string) *adminAPIFixture {
	t.Helper()
	fixture := newAdminAPIFixture(t, client)
	metrics := service.NewMetrics(metricnoop.NewMeterProvider())
	engine := service.NewEngine(
		fixture.store,
		bilibili.New(client, "web-api-test", bilibili.WithBaseURLs(baseURL, baseURL)),
		slog.New(slog.NewTextHandler(io.Discard, nil)), metrics, webTestSettings(), fixture.events, nil,
		service.WithNotificationHTTPClient(client),
	)
	settingsManager := &webTestSettingsManager{engine: engine, store: fixture.store, events: fixture.events}
	fixture.engine = engine
	fixture.server.engine = engine
	fixture.server.settings = settingsManager
	fixture.server.metrics = metrics
	return fixture
}

func runWebTestEngine(t *testing.T, engine *service.Engine) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()
	require.Eventually(t, engine.Running, time.Second, time.Millisecond)
	return func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(3 * time.Second):
			assert.Fail(t, "engine did not stop")
		}
	}
}

func rewriteHTTPClient(server *httptest.Server) *http.Client {
	baseTransport := server.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		clone.Host = clone.URL.Host
		return baseTransport.RoundTrip(clone)
	})}
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

func TestZSXQAPIErrorMappingAndTokenImport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "rate limited", err: zsxq.ErrRateLimited, status: http.StatusTooManyRequests, code: "rate_limited"},
		{name: "authentication", err: zsxq.ErrAuthentication, status: http.StatusUnprocessableEntity, code: "invalid_token"},
		{name: "permission", err: zsxq.ErrPermission, status: http.StatusForbidden, code: "permission_denied"},
		{name: "schema drift", err: zsxq.ErrSchemaDrift, status: http.StatusBadGateway, code: "upstream_failure"},
		{name: "upstream", err: zsxq.ErrUpstream, status: http.StatusBadGateway, code: "upstream_failure"},
		{name: "internal", err: assert.AnError, status: http.StatusInternalServerError, code: "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			writeZSXQAPIError(response, tt.err)
			assertAPIError(t, response, tt.status, tt.code)
		})
	}

	t.Run("token endpoint", func(t *testing.T) {
		t.Parallel()
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v3/users/self":
				if r.Header.Get("Authorization") == "bad" {
					_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": false, "code": 10001})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": true, "code": 0, "resp_data": map[string]any{"user": map[string]any{"uid": 9, "name": "Member"}}})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(upstream.Close)

		fixture := newAdminAPIFixture(t, nil)
		client, err := zsxq.New(upstream.Client(), "web-zsxq-test", zsxq.WithBaseURL(upstream.URL))
		require.NoError(t, err)
		manager, err := zsxq.NewAccountManager(client, fixture.store)
		require.NoError(t, err)
		fixture.server.zsxqAccounts = manager

		missing := fixture.request(t, http.MethodPost, "/api/v4/accounts/zsxq/token", map[string]any{"cookie": "foo=bar"}, true)
		assertAPIError(t, missing, http.StatusBadRequest, "validation_failed")

		authFailed := fixture.request(t, http.MethodPost, "/api/v4/accounts/zsxq/token", map[string]any{"cookie": "zsxq_access_token=bad"}, true)
		assertAPIError(t, authFailed, http.StatusUnprocessableEntity, "invalid_token")
		created := fixture.request(t, http.MethodPost, "/api/v4/accounts/zsxq/token", map[string]any{"cookie": "foo=bar; zsxq_access_token=session-secret"}, true)
		assert.Equal(t, http.StatusCreated, created.Code)
		assert.NotContains(t, created.Body.String(), "session-secret")

		unavailable := newAdminAPIFixture(t, nil)
		response := unavailable.request(t, http.MethodPost, "/api/v4/accounts/zsxq/token", map[string]any{"cookie": "zsxq_access_token=secret"}, true)
		assertAPIError(t, response, http.StatusServiceUnavailable, "integration_unavailable")
	})
}

func webTestSettings() model.RuntimeSettings {
	settings := model.DefaultRuntimeSettings()
	settings.BilibiliRequestRate = 10
	return settings
}

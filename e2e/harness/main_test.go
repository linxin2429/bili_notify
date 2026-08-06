package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplicationHTTPIntegration(t *testing.T) {
	t.Parallel()

	root, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	capture := new(logCapture)
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(io.Discard, capture), &slog.HandlerOptions{Level: slog.LevelWarn}))
	upstream, err := newUpstreamState()
	require.NoError(t, err)
	t.Cleanup(upstream.Close)
	manager := &applicationManager{
		root: root, dataDir: t.TempDir(), upstream: upstream, logger: logger,
		fatal: make(chan error, 1),
	}
	upstream.application = manager
	require.NoError(t, manager.startInitial())
	t.Cleanup(func() { require.NoError(t, manager.stop()) })

	setupCode, err := capture.waitSetupCode(root, 10*time.Second)
	require.NoError(t, err)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{ //nolint:gosec -- generated test certificate
			InsecureSkipVerify: true,
		}},
		Timeout: 5 * time.Second,
	}
	t.Cleanup(client.CloseIdleConnections)
	adminURL := "https://" + manager.adminAddr
	observeURL := "http://" + manager.observeAddr

	response := integrationRequest(t, client, http.MethodGet, observeURL+"/healthz", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	closeIntegrationResponse(t, response)

	response = integrationRequest(t, client, http.MethodGet, adminURL+"/api/v1/session", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	var initial map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&initial))
	closeIntegrationResponse(t, response)
	assert.Equal(t, true, initial["setup_required"])
	assert.Equal(t, false, initial["authenticated"])

	response = integrationRequest(t, client, http.MethodPost, adminURL+"/api/v1/setup", "", map[string]any{
		"setup_code": setupCode,
		"password":   "correct horse battery staple",
	})
	assert.Equal(t, http.StatusOK, response.StatusCode)
	var session map[string]string
	require.NoError(t, json.NewDecoder(response.Body).Decode(&session))
	closeIntegrationResponse(t, response)
	csrf := session["csrf_token"]
	require.NotEmpty(t, csrf)

	response = integrationRequest(t, client, http.MethodPost, adminURL+"/api/v1/channels", csrf, map[string]any{
		"name": "integration robot", "type": "wecom", "enabled": true,
		"settings": map[string]string{}, "secrets": map[string]string{"webhook": upstream.server.URL + "/webhook"},
	})
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	var channel map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&channel))
	closeIntegrationResponse(t, response)
	assert.NotEmpty(t, channel["id"])
	assert.NotContains(t, channel, "secrets")

	response = integrationRequest(t, client, http.MethodPost, adminURL+"/api/v1/ups", csrf, map[string]any{
		"uid": "42", "name": "integration UP", "enabled": true,
	})
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	closeIntegrationResponse(t, response)

	response = integrationRequest(t, client, http.MethodGet, adminURL+"/api/v1/dashboard", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	var dashboard struct {
		UPs      []map[string]any `json:"ups"`
		Channels []map[string]any `json:"channels"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&dashboard))
	closeIntegrationResponse(t, response)
	assert.Len(t, dashboard.UPs, 1)
	assert.Len(t, dashboard.Channels, 1)

	response = integrationRequest(t, client, http.MethodGet, adminURL+"/api/v1/audit-logs?limit=100", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	var auditPage struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&auditPage))
	closeIntegrationResponse(t, response)
	assert.GreaterOrEqual(t, auditPage.Total, 3)
	assert.NotEmpty(t, auditPage.Items)

	require.NoError(t, manager.Restart())
	response = integrationRequest(t, client, http.MethodGet, adminURL+"/api/v1/session", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	var restarted map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&restarted))
	closeIntegrationResponse(t, response)
	assert.Equal(t, false, restarted["setup_required"])
	assert.Equal(t, false, restarted["authenticated"])

	response = integrationRequest(t, client, http.MethodGet, observeURL+"/metrics", "", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	closeIntegrationResponse(t, response)
	assert.Contains(t, string(body), "bili_notify_auth_state")
}

func integrationRequest(t *testing.T, client *http.Client, method, endpoint, csrf string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, reader)
	require.NoError(t, err)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	return response
}

func closeIntegrationResponse(t *testing.T, response *http.Response) {
	t.Helper()
	require.NoError(t, response.Body.Close())
}

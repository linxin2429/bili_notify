package web

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/linxin2429/bili_notify/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticationHTTPLifecycle(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	server := &Server{auth: auth, store: store, events: service.NewEventBus(), connections: make(map[string]map[*websocket.Conn]struct{})}
	handler := server.adminHandler()

	response := authenticationRequest(t, handler, http.MethodGet, "/api/v1/session", nil, "", "")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"setup_required":true,"authenticated":false}`, response.Body.String())

	response = authenticationRequest(t, handler, http.MethodPost, "/api/v1/setup", map[string]string{
		"setup_code": "wrong", "password": "correct horse battery staple",
	}, "", "")
	assertAPIError(t, response, http.StatusBadRequest, "invalid_setup")

	response = authenticationRequest(t, handler, http.MethodPost, "/api/v1/setup", map[string]string{
		"setup_code": setupCode, "password": "correct horse battery staple",
	}, "", "")
	assert.Equal(t, http.StatusOK, response.Code)
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &login))
	require.NotEmpty(t, login.CSRF)
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	token := cookies[0].Value
	assert.True(t, cookies[0].Secure)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)

	response = authenticationRequest(t, handler, http.MethodGet, "/api/v1/session", nil, token, "")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"authenticated":true`)
	assert.Contains(t, response.Body.String(), login.CSRF)

	response = authenticationRequest(t, handler, http.MethodPut, "/api/v1/session/password", map[string]string{
		"current_password": "wrong", "new_password": "replacement horse battery staple",
	}, token, login.CSRF)
	assertAPIError(t, response, http.StatusBadRequest, "invalid_password")

	response = authenticationRequest(t, handler, http.MethodPut, "/api/v1/session/password", map[string]string{
		"current_password": "correct horse battery staple", "new_password": "replacement horse battery staple",
	}, token, login.CSRF)
	assert.Equal(t, http.StatusOK, response.Code)
	var replacement struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replacement))
	require.NotEmpty(t, replacement.CSRF)
	newCookies := response.Result().Cookies()
	require.Len(t, newCookies, 1)
	newToken := newCookies[0].Value
	assert.NotEqual(t, token, newToken)

	response = authenticationRequest(t, handler, http.MethodGet, "/api/v1/dashboard", nil, token, "")
	assertAPIError(t, response, http.StatusUnauthorized, "authentication_required")
	response = authenticationRequest(t, handler, http.MethodDelete, "/api/v1/session", nil, newToken, replacement.CSRF)
	assert.Equal(t, http.StatusNoContent, response.Code)
	require.Len(t, response.Result().Cookies(), 1)
	assert.Equal(t, -1, response.Result().Cookies()[0].MaxAge)

	response = authenticationRequest(t, handler, http.MethodPost, "/api/v1/session", map[string]string{"password": "correct horse battery staple"}, "", "")
	assertAPIError(t, response, http.StatusUnauthorized, "invalid_credentials")
	response = authenticationRequest(t, handler, http.MethodPost, "/api/v1/session", map[string]string{"password": "replacement horse battery staple"}, "", "")
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestAuthenticationRateLimit(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	server := &Server{auth: auth, store: store, events: service.NewEventBus(), connections: make(map[string]map[*websocket.Conn]struct{})}
	handler := server.adminHandler()
	for range 5 {
		response := authenticationRequest(t, handler, http.MethodPost, "/api/v1/session", map[string]string{"password": "wrong"}, "", "")
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	}
	response := authenticationRequest(t, handler, http.MethodPost, "/api/v1/session", map[string]string{"password": "correct horse battery staple"}, "", "")
	assertAPIError(t, response, http.StatusTooManyRequests, "rate_limited")
}

func TestAuthenticationRateLimitIsolationRecoveryAndProxyHeaders(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	now := time.Date(2026, time.August, 9, 1, 2, 3, 0, time.UTC)
	auth.now = func() time.Time { return now }
	server := &Server{auth: auth, store: store, events: service.NewEventBus(), connections: make(map[string]map[*websocket.Conn]struct{})}
	handler := server.adminHandler()

	requestLogin := func(remote, forwardedFor, password string) *httptest.ResponseRecorder {
		body := strings.NewReader(`{"password":"` + password + `"}`)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/session", body)
		request.RemoteAddr = remote
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", forwardedFor)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for range 5 {
		assert.Equal(t, http.StatusUnauthorized, requestLogin("192.0.2.1:1000", "198.51.100.99", "wrong").Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, requestLogin("192.0.2.1:1001", "203.0.113.7", "correct horse battery staple").Code)
	assert.Equal(t, http.StatusOK, requestLogin("192.0.2.2:1000", "192.0.2.1", "correct horse battery staple").Code,
		"rate limiting must use the peer address and ignore spoofable forwarding headers")
	now = now.Add(time.Minute)
	assert.Equal(t, http.StatusOK, requestLogin("192.0.2.1:1002", "203.0.113.7", "correct horse battery staple").Code)
}

func TestAdminSecurityHeaders(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	fixture.server.static = static
	tests := []struct {
		name string
		path string
	}{
		{name: "HTML", path: "/"},
		{name: "API", path: "/api/v1/session"},
		{name: "missing API", path: "/api/v1/missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()
			fixture.server.adminHandler().ServeHTTP(response, request)
			assert.Equal(t, "max-age=31536000", response.Header().Get("Strict-Transport-Security"))
			assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-referrer", response.Header().Get("Referrer-Policy"))
			csp := response.Header().Get("Content-Security-Policy")
			assert.Contains(t, csp, "default-src 'self'")
			assert.Contains(t, csp, "frame-ancestors 'none'")
			assert.Contains(t, csp, "form-action 'self'")
		})
	}
}

func TestJSONBodySizeAndEncodingBoundaries(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	tests := []struct {
		name string
		body []byte
	}{
		{name: "body larger than one MiB", body: append([]byte(`{"uid":"42","name":"up","enabled":true}`), []byte(strings.Repeat(" ", 1<<20))...)},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'u', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/ups", bytes.NewReader(tt.body))
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: fixture.token})
			request.Header.Set("X-CSRF-Token", fixture.csrf)
			response := httptest.NewRecorder()
			fixture.server.adminHandler().ServeHTTP(response, request)
			assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestEncodedAPIPathsCannotEscapeTheirRoute(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	tests := []struct {
		name string
		path string
	}{
		{name: "encoded traversal", path: "/api/v1/dynamics/%2e%2e%2fsession"},
		{name: "encoded NUL", path: "/api/v1/dynamics/%00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			request.AddCookie(&http.Cookie{Name: sessionCookie, Value: fixture.token})
			response := httptest.NewRecorder()
			fixture.server.adminHandler().ServeHTTP(response, request)
			assert.Equal(t, http.StatusNotFound, response.Code)
			assert.NotContains(t, response.Body.String(), "csrf_token")
			assert.NotContains(t, response.Body.String(), fixture.csrf)
		})
	}
}

func TestIndexAndUnknownAPIRoutes(t *testing.T) {
	t.Parallel()
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	server := &Server{static: static}
	tests := []struct {
		name        string
		path        string
		status      int
		contentType string
		contains    string
	}{
		{name: "root serves UI", path: "/", status: http.StatusOK, contentType: "text/html; charset=utf-8", contains: `<div id="root"></div>`},
		{name: "client route serves UI", path: "/history", status: http.StatusOK, contentType: "text/html; charset=utf-8", contains: `<div id="root"></div>`},
		{name: "unknown API stays JSON", path: "/api/v1/missing", status: http.StatusNotFound, contentType: "application/json; charset=utf-8", contains: `"not_found"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()
			server.index(response, request)
			assert.Equal(t, tt.status, response.Code)
			assert.Equal(t, tt.contentType, response.Header().Get("Content-Type"))
			assert.Contains(t, response.Body.String(), tt.contains)
		})
	}
}

func authenticationRequest(t *testing.T, handler http.Handler, method, path string, body any, token, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.RemoteAddr = "198.51.100.20:4321"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

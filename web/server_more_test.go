package web

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

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

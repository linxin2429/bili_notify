package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRefreshProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		required bool
	}{{"not due", false}, {"renew", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var refreshed, confirmed atomic.Bool
			parseForm := checkedSessionFormParser(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, err := r.Cookie("SESSDATA")
				if !assert.NoError(t, err) {
					w.WriteHeader(400)
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/cookie/info"):
					assert.Equal(t, "old", cookie.Value)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"refresh": tt.required, "timestamp": 123456789}})
				case strings.HasPrefix(r.URL.Path, "/correspond/1/"):
					assert.Len(t, strings.TrimPrefix(r.URL.Path, "/correspond/1/"), 256)
					assert.Equal(t, "old", cookie.Value)
					_, _ = io.WriteString(w, `<html><div class="token" id='1-name'>refresh-csrf</div></html>`)
				case strings.HasSuffix(r.URL.Path, "/cookie/refresh"):
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
					if !parseForm(w, r) {
						return
					}
					assert.Equal(t, "old-csrf", r.Form.Get("csrf"))
					assert.Equal(t, "refresh-csrf", r.Form.Get("refresh_csrf"))
					assert.Equal(t, "old-token", r.Form.Get("refresh_token"))
					assert.Equal(t, "main_web", r.Form.Get("source"))
					http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "new"})
					http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "new-csrf"})
					http.SetCookie(w, &http.Cookie{Name: "remove", Value: "", MaxAge: -1})
					_, _ = io.WriteString(w, `{"code":0,"data":{"refresh_token":"new-token"}}`)
					refreshed.Store(true)
				case strings.HasSuffix(r.URL.Path, "/nav"):
					assert.Equal(t, "new", cookie.Value)
					_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"user"}}`)
				case strings.HasSuffix(r.URL.Path, "/confirm/refresh"):
					assert.Equal(t, "new", cookie.Value)
					if !parseForm(w, r) {
						return
					}
					assert.Equal(t, "new-csrf", r.Form.Get("csrf"))
					assert.Equal(t, "old-token", r.Form.Get("refresh_token"))
					_, _ = io.WriteString(w, `{"code":0}`)
					confirmed.Store(true)
				default:
					t.Errorf("unexpected operation: %s", r.Method)
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL), WithWebURL(server.URL))
			original := model.BiliSession{Cookies: map[string]string{"SESSDATA": "old", "bili_jct": "old-csrf", "keep": "yes", "remove": "yes"}, RefreshToken: "old-token"}
			client.SetSession(original)
			protocol := NewSessionClient(client)
			info, err := protocol.Check(t.Context(), original)
			require.NoError(t, err)
			assert.Equal(t, tt.required, info.Required)
			if tt.required {
				next, err := protocol.Refresh(t.Context(), original, info.Timestamp)
				require.NoError(t, err)
				assert.Equal(t, "new-token", next.RefreshToken)
				assert.Equal(t, "yes", next.Cookies["keep"])
				assert.NotContains(t, next.Cookies, "remove")
				account, err := protocol.Validate(t.Context(), next)
				require.NoError(t, err)
				assert.Equal(t, "42", account.UID)
				next.PendingRefreshToken = original.RefreshToken
				require.NoError(t, protocol.Confirm(t.Context(), next))
			}
			assert.Equal(t, tt.required, refreshed.Load())
			assert.Equal(t, tt.required, confirmed.Load())
			assert.Equal(t, "old", original.Cookies["SESSDATA"])
			assert.Equal(t, "old", client.cookies["SESSDATA"])
		})
	}
}

func TestSessionProtocolFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, operation, body string
		status                int
		kind                  ErrorKind
		rejected              bool
	}{
		{name: "missing flag", body: `{"code":0,"data":{}}`, kind: ErrorSchema},
		{name: "missing timestamp", body: `{"code":0,"data":{"refresh":true}}`, kind: ErrorSchema},
		{name: "missing code", body: `{"data":{"refresh":false}}`, kind: ErrorSchema},
		{name: "null data", body: `{"code":0,"data":null}`, kind: ErrorSchema},
		{name: "malformed", body: `{"secret":"DO-NOT-LOG"`, kind: ErrorSchema},
		{name: "rejected", body: `{"code":86095,"message":"DO-NOT-LOG"}`, rejected: true},
		{name: "csrf rejected", body: `{"code":-111}`, rejected: true},
		{name: "logged out", body: `{"code":-101}`, kind: ErrorAuthentication},
		{name: "risk", body: `{"code":-352}`, kind: ErrorRiskControl},
		{name: "unknown code", body: `{"code":123,"message":"DO-NOT-LOG"}`, kind: ErrorTemporary},
		{name: "rate limited", status: 429, kind: ErrorRiskControl},
		{name: "forbidden", status: 403, kind: ErrorRiskControl},
		{name: "unauthorized", status: 401, kind: ErrorAuthentication},
		{name: "server error", status: 503, kind: ErrorTemporary},
		{name: "redirect refused", status: 302, kind: ErrorTemporary},
		{name: "oversized", body: strings.Repeat("x", (1<<20)+1), kind: ErrorSchema},
		{name: "missing csrf html", operation: "refresh", body: `<div>none</div>`, kind: ErrorSchema},
		{name: "missing renewed cookies", operation: "refresh", body: `{"code":0,"data":{"refresh_token":"new"}}`, kind: ErrorSchema},
		{name: "invalid login", operation: "validate", body: `{"code":0,"data":{"isLogin":false}}`, kind: ErrorAuthentication},
		{name: "missing login", operation: "validate", body: `{"code":0,"data":{"mid":42}}`, kind: ErrorSchema},
		{name: "missing uid", operation: "validate", body: `{"code":0,"data":{"isLogin":true}}`, kind: ErrorSchema},
		{name: "confirmation rejected", operation: "confirm", body: `{"code":-111}`, rejected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.operation == "refresh" && strings.HasPrefix(r.URL.Path, "/correspond/") && tt.name != "missing csrf html" {
					_, _ = io.WriteString(w, `<div id="1-name">csrf</div>`)
					return
				}
				if tt.status != 0 {
					w.Header().Set("Retry-After", "120")
					w.Header().Set("Location", "https://never-follow.invalid/secret")
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			protocol := NewSessionClient(New(server.Client(), "test", WithBaseURLs(server.URL, server.URL), WithWebURL(server.URL)))
			session := model.BiliSession{Cookies: map[string]string{"SESSDATA": "secret", "bili_jct": "secret"}, RefreshToken: "secret"}
			var err error
			switch tt.operation {
			case "refresh":
				_, err = protocol.Refresh(t.Context(), session, 100)
			case "validate":
				_, err = protocol.Validate(t.Context(), session)
			case "confirm":
				err = protocol.Confirm(t.Context(), session)
			default:
				_, err = protocol.Check(t.Context(), session)
			}
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "DO-NOT-LOG")
			if tt.rejected {
				assert.ErrorIs(t, err, ErrRefreshRejected)
			} else {
				apiErr, ok := errors.AsType[*APIError](err)
				require.True(t, ok)
				assert.Equal(t, tt.kind, apiErr.Kind)
				if tt.status > 0 {
					assert.Equal(t, 2*time.Minute, apiErr.RetryAfter)
				}
			}
		})
	}
}

func TestSessionProtocolTransportFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cancel bool
	}{{"canceled", true}, {"sanitized transport", false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.cancel {
				cancel()
			}
			client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-url-and-token") })}, "test")
			_, err := NewSessionClient(client).Check(ctx, model.BiliSession{})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-url-and-token")
			if tt.cancel {
				assert.ErrorIs(t, err, context.Canceled)
			} else {
				apiErr, ok := errors.AsType[*APIError](err)
				require.True(t, ok)
				assert.Equal(t, ErrorTemporary, apiErr.Kind)
			}
		})
	}
}

// HTTP handlers run outside the test goroutine: stop processing immediately,
// then report prerequisites with require from the test's cleanup goroutine.
func checkedSessionFormParser(t *testing.T) func(http.ResponseWriter, *http.Request) bool {
	t.Helper()
	var mu sync.Mutex
	var parseErrors []error
	t.Cleanup(func() {
		mu.Lock()
		err := errors.Join(parseErrors...)
		mu.Unlock()
		require.NoError(t, err, "parsing mock request form")
	})
	return func(w http.ResponseWriter, r *http.Request) bool {
		if err := r.ParseForm(); err != nil {
			mu.Lock()
			parseErrors = append(parseErrors, err)
			mu.Unlock()
			http.Error(w, "invalid request form", http.StatusBadRequest)
			return false
		}
		return true
	}
}

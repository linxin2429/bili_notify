package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func renewalEngine(t *testing.T, server *httptest.Server) *Engine {
	t.Helper()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL), bilibili.WithWebURL(server.URL))
	engine := NewEngine(store, client, testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	session := model.BiliSession{AccountUID: "42", AccountName: "User", Cookies: map[string]string{"SESSDATA": "old", "bili_jct": "old-csrf"}, RefreshToken: "old-token"}
	require.NoError(t, store.SaveSession(session))
	client.SetSession(session)
	engine.setAccount(model.BiliAccount{UID: "42", Name: "User"})
	engine.authValid.Store(true)
	return engine
}

func TestSessionValidationDoesNotInvalidateTransientFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		valid  bool
		retry  bool
	}{
		{name: "temporary", status: 503, valid: true, retry: true},
		{name: "rate limit", status: 429, valid: true, retry: true},
		{name: "schema", body: `{"code":0,"data":{}}`, valid: true},
		{name: "logged out", body: `{"code":0,"data":{"isLogin":false}}`, valid: false},
		{name: "business authentication", body: `{"code":-101}`, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.Header().Set("Retry-After", "120")
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			engine := renewalEngine(t, server)
			engine.maintainBiliSession(t.Context())
			assert.Equal(t, tt.valid, engine.authValid.Load())
			account, err := engine.store.PlatformAccount(model.PlatformBilibili)
			require.NoError(t, err)
			if tt.valid {
				assert.Equal(t, model.AccountConnected, account.Status)
			} else {
				assert.Equal(t, model.AccountInvalid, account.Status)
			}
			if tt.retry {
				assert.True(t, engine.sessionRetryAt.After(time.Now()))
			}
		})
	}
}

func TestStartupValidationDefersTransientFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		status     int
		want       model.AccountStatus
	}{
		{name: "network", status: 503, want: model.AccountConnected},
		{name: "schema", body: `{}`, want: model.AccountConnected},
		{name: "authentication", body: `{"code":-101}`, want: model.AccountInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			engine := renewalEngine(t, server)
			engine.authValid.Store(false)
			require.NoError(t, engine.restoreBiliSession(t.Context()))
			account, err := engine.store.PlatformAccount(model.PlatformBilibili)
			require.NoError(t, err)
			assert.Equal(t, tt.want, account.Status)
		})
	}
}

func TestRenewalResumesConfirmation(t *testing.T) {
	t.Parallel()
	var checks, refreshes, confirms atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nav"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"User"}}`)
		case strings.HasSuffix(r.URL.Path, "/cookie/info"):
			checks.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"data":{"refresh":true,"timestamp":123}}`)
		case strings.HasPrefix(r.URL.Path, "/correspond/"):
			_, _ = io.WriteString(w, `<div id="1-name">csrf</div>`)
		case strings.HasSuffix(r.URL.Path, "/cookie/refresh"):
			refreshes.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "new"})
			http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "new-csrf"})
			_, _ = io.WriteString(w, `{"code":0,"data":{"refresh_token":"new-token"}}`)
		case strings.HasSuffix(r.URL.Path, "/confirm/refresh"):
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "old-token", r.Form.Get("refresh_token"))
			if confirms.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = io.WriteString(w, `{"code":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	engine := renewalEngine(t, server)
	require.NoError(t, engine.store.PutUP(model.UP{UID: "7", Enabled: true, BaselineReady: true}))
	before, err := engine.store.FeedState("42")
	require.NoError(t, err)
	engine.maintainBiliSession(t.Context())
	session, err := engine.store.Session()
	require.NoError(t, err)
	assert.Equal(t, "new-token", session.RefreshToken)
	assert.Equal(t, "old-token", session.PendingRefreshToken)
	// A new engine restores the durable pending state, just as on restart.
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL), bilibili.WithWebURL(server.URL))
	restarted := NewEngine(engine.store, client, testLogger(), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	require.NoError(t, restarted.restoreBiliSession(t.Context()))
	restarted.maintainBiliSession(t.Context())
	session, err = engine.store.Session()
	require.NoError(t, err)
	assert.Empty(t, session.PendingRefreshToken)
	assert.EqualValues(t, 1, checks.Load())
	assert.EqualValues(t, 1, refreshes.Load())
	assert.EqualValues(t, 2, confirms.Load())
	after, err := engine.store.FeedState("42")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	up, err := engine.store.UP("7")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)
}

func TestRenewalSerializesAccountChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		logout bool
	}{{name: "logout", logout: true}, {name: "new QR account"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{})
			release := make(chan struct{})
			var unblock sync.Once
			t.Cleanup(func() { unblock.Do(func() { close(release) }) })
			var checks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/nav"):
					cookie, _ := r.Cookie("SESSDATA")
					uid := "42"
					if cookie != nil && cookie.Value == "qr-new" {
						uid = "99"
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":`+uid+`,"uname":"User"}}`)
				case strings.HasSuffix(r.URL.Path, "/cookie/info"):
					if checks.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"refresh":false}}`)
				case strings.HasSuffix(r.URL.Path, "/qrcode/poll"):
					http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "qr-new"})
					http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "qr-csrf"})
					_, _ = io.WriteString(w, `{"code":0,"data":{"code":0,"refresh_token":"qr-token"}}`)
				default:
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			engine := renewalEngine(t, server)
			engine.login = &LoginSession{Key: "qr", ExpiresAt: time.Now().Add(time.Minute)}
			done := make(chan struct{})
			go func() { engine.maintainBiliSession(t.Context()); close(done) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("maintenance did not enter")
			}
			changed := make(chan error, 1)
			go func() {
				if tt.logout {
					changed <- engine.ClearBilibiliSession()
				} else {
					_, err := engine.PollLogin(t.Context(), "qr")
					changed <- err
				}
			}()
			// A reader cannot observe a session while maintenance owns its write lock.
			if acquired := engine.sessionMu.TryRLock(); acquired {
				engine.sessionMu.RUnlock()
				t.Error("maintenance must own the session lock")
			}
			unblock.Do(func() { close(release) })
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("maintenance did not finish")
			}
			select {
			case err := <-changed:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("account change blocked")
			}
			session, err := engine.store.Session()
			if tt.logout {
				assert.ErrorIs(t, err, state.ErrNotFound)
				assert.False(t, engine.authValid.Load())
				engine.maintainBiliSession(t.Context())
				_, err = engine.store.Session()
				assert.ErrorIs(t, err, state.ErrNotFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "99", session.AccountUID)
				assert.Equal(t, "qr-token", session.RefreshToken)
				assert.Equal(t, "qr-new", session.Cookies["SESSDATA"])
			}
		})
	}
}

func TestMissingRenewalCredentialsPreserveLogin(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"User"}}`)
	}))
	t.Cleanup(server.Close)
	engine := renewalEngine(t, server)
	session, err := engine.store.Session()
	require.NoError(t, err)
	session.RefreshToken = ""
	require.NoError(t, engine.store.SaveSession(session))
	engine.maintainBiliSession(t.Context())
	assert.True(t, engine.sessionWarning)
	revision := engine.events.Revision()
	engine.maintainBiliSession(t.Context())
	assert.Equal(t, revision, engine.events.Revision())
	assert.True(t, engine.authValid.Load())
}

func TestCompletedQRDoesNotOverwriteRenewal(t *testing.T) {
	t.Parallel()
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/qrcode/poll") {
			polls.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "qr-new"})
			_, _ = io.WriteString(w, `{"code":0,"data":{"code":0,"refresh_token":"qr-token"}}`)
		} else {
			_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"User"}}`)
		}
	}))
	t.Cleanup(server.Close)
	engine := renewalEngine(t, server)
	engine.login = &LoginSession{Key: "qr", ExpiresAt: time.Now().Add(time.Minute)}
	first, err := engine.PollLogin(t.Context(), "qr")
	require.NoError(t, err)
	session, err := engine.store.Session()
	require.NoError(t, err)
	session.Cookies["SESSDATA"] = "renewed-after-qr"
	session.RefreshToken = "renewed-token"
	require.NoError(t, engine.store.SaveSession(session))
	engine.client.SetSession(session)
	second, err := engine.PollLogin(t.Context(), "qr")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.EqualValues(t, 1, polls.Load())
	got, err := engine.store.Session()
	require.NoError(t, err)
	assert.Equal(t, "renewed-token", got.RefreshToken)
	assert.Equal(t, "renewed-after-qr", got.Cookies["SESSDATA"])
}

package bilibili

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpusDeviceCookieLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		device              string
		wantInitializations int32
	}{
		{name: "QR session missing device", wantInitializations: 1},
		{name: "blank device", device: " ", wantInitializations: 1},
		{name: "existing device", device: "existing-device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var initializations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/frontend/finger/spi" {
					initializations.Add(1)
					assert.Empty(t, r.Header.Get("Cookie"), "device bootstrap does not need account credentials")
					_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"new-device"}}`)
					return
				}
				assert.Equal(t, "/x/polymer/web-dynamic/v1/opus/detail", r.URL.Path)
				session, err := r.Cookie("SESSDATA")
				if !assert.NoError(t, err) {
					http.Error(w, "missing login", 401)
					return
				}
				assert.Equal(t, r.URL.Query().Get("id"), session.Value)
				device, err := r.Cookie("buvid3")
				if !assert.NoError(t, err) {
					_, _ = io.WriteString(w, `{"code":4101131}`)
					return
				}
				want := "new-device"
				if tt.device == "existing-device" {
					want = tt.device
				}
				assert.Equal(t, want, device.Value)
				assert.Equal(t, 1, strings.Count(r.Header.Get("Cookie"), "buvid3="))
				_, _ = io.WriteString(w, opusDetailEnvelope(opusDetailItem(session.Value, "paid article", []string{opusTextParagraph("full paid body")})))
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
			client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "original", "buvid3": tt.device}})
			var wg sync.WaitGroup
			results := make(chan error, 12)
			for range cap(results) {
				wg.Go(func() { _, err := client.fetchOpusDetail(t.Context(), "original"); results <- err })
			}
			wg.Wait()
			close(results)
			for err := range results {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantInitializations, initializations.Load())
			// Renewal and logout must not restore old credentials or discard a
			// separately initialized device identifier.
			client.ClearSession()
			client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "renewed", "buvid3": tt.device}})
			got, err := client.fetchOpusDetail(t.Context(), "renewed")
			require.NoError(t, err)
			assert.Equal(t, "full paid body", got.Body)
			assert.Equal(t, tt.wantInitializations, initializations.Load())
		})
	}
}

func TestDeviceInitializationFailureCanRecover(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		status     int
		kind       ErrorKind
	}{
		{name: "HTTP failure", status: 503, kind: ErrorTemporary},
		{name: "risk control", body: `{"code":-352}`, kind: ErrorRiskControl},
		{name: "invalid JSON", body: `{`, kind: ErrorSchema},
		{name: "missing device", body: `{"code":0,"data":{}}`, kind: ErrorSchema},
		{name: "empty device", body: `{"code":0,"data":{"b_3":" "}}`, kind: ErrorSchema},
		{name: "invalid cookie", body: `{"code":0,"data":{"b_3":"secret;injected=1"}}`, kind: ErrorSchema},
		{name: "oversized cookie", body: fmt.Sprintf(`{"code":0,"data":{"b_3":%q}}`, strings.Repeat("x", 257)), kind: ErrorSchema},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			var details atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/x/frontend/finger/spi" {
					details.Add(1)
					_, _ = io.WriteString(w, opusDetailEnvelope(opusDetailItem("1", "t", []string{opusTextParagraph("full")})))
					return
				}
				if recovered.Load() {
					_, _ = io.WriteString(w, `{"code":0,"data":{"b_3":"valid-device"}}`)
					return
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
			_, err := client.fetchOpusDetail(t.Context(), "1")
			require.Error(t, err)
			apiErr, ok := errors.AsType[*APIError](err)
			require.True(t, ok)
			assert.Equal(t, tt.kind, apiErr.Kind)
			assert.NotContains(t, err.Error(), "secret")
			assert.Zero(t, details.Load())
			recovered.Store(true)
			_, err = client.fetchOpusDetail(t.Context(), "1")
			require.NoError(t, err)
			assert.Equal(t, int32(1), details.Load())
		})
	}
}

func TestDeviceInitializationCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		waiting bool
	}{
		{name: "waiting for initializer", waiting: true},
		{name: "HTTP request canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := New(nil, "test", WithBaseURLs("http://127.0.0.1:1", "http://127.0.0.1:1"))
			if tt.waiting {
				client.deviceInit <- struct{}{}
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			cancel()
			_, err := client.fetchOpusDetail(ctx, "1")
			require.ErrorIs(t, err, context.Canceled)
			assert.False(t, client.hasDeviceCookie())
		})
	}
}

func TestOpusPaidPreviewIsNotFullContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		paywall bool
	}{
		{name: "entitled full article"},
		{name: "preview with paywall", paywall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			item := opusDetailItem("1", "paid", []string{opusTextParagraph("text")})
			item = strings.Replace(item, `"basic":{`, `"basic":{"is_only_fans":true,`, 1)
			if tt.paywall {
				item = strings.Replace(item, `"modules":[`, `"modules":[{"module_type":"MODULE_TYPE_PAYWALL","module_paywall":{"preview_ratio":55}},`, 1)
			}
			got, err := parseOpusDetail("1", []byte(opusDetailEnvelope(item)))
			if !tt.paywall {
				require.NoError(t, err)
				assert.Equal(t, "text", got.Body)
				return
			}
			require.Error(t, err)
			apiErr, ok := errors.AsType[*APIError](err)
			require.True(t, ok)
			assert.Equal(t, ErrorContentAccess, apiErr.Kind)
			assert.Empty(t, got.Body)
			assert.False(t, IsAuthentication(err))
			assert.False(t, IsDynamicBlocked(err), "inaccessible details must remain pending")
		})
	}
}

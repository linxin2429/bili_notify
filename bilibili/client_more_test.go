package bilibili

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEnvelopeClassifiesResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		want     int
		wantKind ErrorKind
	}{
		{name: "success", body: `{"code":0,"data":{"value":42}}`, want: 42},
		{name: "invalid envelope", body: `{`, wantKind: ErrorSchema},
		{name: "authentication", body: `{"code":-101,"message":"login required"}`, wantKind: ErrorAuthentication},
		{name: "risk control", body: `{"code":-412,"message":"blocked"}`, wantKind: ErrorRiskControl},
		{name: "temporary", body: `{"code":-500,"message":"failed"}`, wantKind: ErrorTemporary},
		{name: "invalid data", body: `{"code":0,"data":{"value":"wrong"}}`, wantKind: ErrorSchema},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got struct {
				Value int `json:"value"`
			}
			err := decodeEnvelope([]byte(tt.body), &got)
			if tt.wantKind == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got.Value)
				return
			}
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.wantKind, apiErr.Kind)
		})
	}
}

func TestClientSessionHeadersAndRetryAfter(t *testing.T) {
	t.Parallel()
	client := New(nil, "contract-agent")
	client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "secret"}})

	tests := []struct {
		name           string
		withAuth       bool
		clear          bool
		retryAfter     string
		wantCookie     bool
		wantRetryAfter time.Duration
	}{
		{name: "authenticated", withAuth: true, retryAfter: "12", wantCookie: true, wantRetryAfter: 12 * time.Second},
		{name: "anonymous", retryAfter: "invalid"},
		{name: "cleared", withAuth: true, clear: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			local := client
			if tt.clear {
				local = New(nil, "contract-agent")
				local.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "secret"}})
				local.ClearSession()
			}
			request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
			require.NoError(t, err)
			local.addHeaders(request, tt.withAuth)
			assert.Equal(t, "contract-agent", request.Header.Get("User-Agent"))
			assert.Equal(t, tt.wantCookie, request.Header.Get("Cookie") != "")

			response := &http.Response{Header: make(http.Header)}
			response.Header.Set("Retry-After", tt.retryAfter)
			assert.Equal(t, tt.wantRetryAfter, ParseRetryAfter(response))
		})
	}
	assert.Zero(t, ParseRetryAfter(nil))
}

func TestClientClassifiesHTTPAndQRStates(t *testing.T) {
	t.Parallel()
	httpTests := []struct {
		name     string
		status   int
		wantKind ErrorKind
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantKind: ErrorAuthentication},
		{name: "rate limited", status: http.StatusTooManyRequests, wantKind: ErrorRiskControl},
		{name: "server error", status: http.StatusInternalServerError, wantKind: ErrorTemporary},
	}
	for _, tt := range httpTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test")
			_, _, err := client.get(t.Context(), server.URL, nil, true)
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.wantKind, apiErr.Kind)
		})
	}

	qrTests := []struct {
		name       string
		code       int
		wantStatus QRStatus
		wantErr    bool
	}{
		{name: "waiting", code: 86101, wantStatus: QRWaiting},
		{name: "scanned", code: 86090, wantStatus: QRScanned},
		{name: "expired", code: 86038, wantStatus: QRExpired},
		{name: "success missing cookie", code: 0, wantErr: true},
		{name: "unknown", code: 12345, wantErr: true},
	}
	for _, tt := range qrTests {
		t.Run("qr "+tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"code":0,"data":{"code":`+fmt.Sprint(tt.code)+`}}`)
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
			status, _, err := client.PollQR(t.Context(), "key")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

func TestIdentityResponseValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		body string
		call func(context.Context, *Client) error
	}{
		{
			name: "session is logged out", path: "/x/web-interface/nav",
			body: `{"code":0,"data":{"isLogin":false}}`,
			call: func(ctx context.Context, client *Client) error { _, err := client.ValidateSession(ctx); return err },
		},
		{
			name: "session identity is missing", path: "/x/web-interface/nav",
			body: `{"code":0,"data":{"isLogin":true,"mid":0}}`,
			call: func(ctx context.Context, client *Client) error { _, err := client.ValidateSession(ctx); return err },
		},
		{
			name: "QR key is missing", path: "/x/passport-login/web/qrcode/generate",
			body: `{"code":0,"data":{"url":"https://example.com/login"}}`,
			call: func(ctx context.Context, client *Client) error { _, err := client.GenerateQR(ctx); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.path, r.URL.Path)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
			err := tt.call(t.Context(), client)
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Contains(t, []ErrorKind{ErrorAuthentication, ErrorSchema}, apiErr.Kind)
		})
	}
}

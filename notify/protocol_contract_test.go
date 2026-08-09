package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errorReader struct {
	data []byte
	err  error
}

func (r *errorReader) Read(buffer []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(buffer, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestRobotProtocolFailureClassification(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       io.Reader
		retryAfter string
		transport  error
		permanent  bool
		wantRetry  time.Duration
		want       string
	}{
		{name: "connection reset", transport: errors.New("connection reset by peer"), want: "connection reset"},
		{name: "truncated body", status: http.StatusOK, body: &errorReader{data: []byte(`{"code":`), err: io.ErrUnexpectedEOF}, want: "reading"},
		{name: "rate limited with delta", status: http.StatusTooManyRequests, body: strings.NewReader(`{"code":1}`), retryAfter: "17", wantRetry: 17 * time.Second, want: "HTTP 429"},
		{name: "server failure", status: http.StatusBadGateway, body: strings.NewReader(`{}`), want: "HTTP 502"},
		{name: "client failure", status: http.StatusUnauthorized, body: strings.NewReader(`{"secret":"must-not-leak"}`), permanent: true, want: "HTTP 401"},
		{name: "malformed JSON", status: http.StatusOK, body: strings.NewReader(`{"code":`), permanent: true, want: "decoding"},
		{name: "missing business code", status: http.StatusOK, body: strings.NewReader(`{"unknown_future_field":true}`), permanent: true, want: "missing business code"},
		{name: "business code type drift", status: http.StatusOK, body: strings.NewReader(`{"code":{"value":0}}`), permanent: true, want: "has type"},
		{name: "business failure", status: http.StatusOK, body: strings.NewReader(`{"errcode":40035,"errmsg":"secret must-not-leak"}`), permanent: true, want: "business code 40035"},
		{name: "oversized success", status: http.StatusOK, body: strings.NewReader(strings.Repeat("x", maxProtocolResponseBytes+1)), permanent: true, want: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if tt.transport != nil {
					return nil, tt.transport
				}
				header := make(http.Header)
				header.Set("Retry-After", tt.retryAfter)
				body := tt.body
				if body == nil {
					body = strings.NewReader(`{}`)
				}
				return &http.Response{StatusCode: tt.status, Header: header, Body: io.NopCloser(body), Request: request}, nil
			})}
			t.Cleanup(client.CloseIdleConnections)
			sender := &robotSender{kind: "wecom", webhook: "https://webhook.invalid/sensitive-token", client: client}
			err := sender.postJSON(t.Context(), sender.webhook, map[string]string{"content": "hello"})
			require.Error(t, err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), "must-not-leak")
			assert.NotContains(t, err.Error(), "sensitive-token")
			delay, ok := RetryAfter(err)
			assert.Equal(t, tt.wantRetry > 0, ok)
			if tt.wantRetry > 0 {
				assert.Equal(t, tt.wantRetry, delay)
			}
		})
	}
}

func TestRetryAfterContracts(t *testing.T) {
	tests := []struct {
		name    string
		header  func() string
		minimum time.Duration
		maximum time.Duration
		present bool
	}{
		{name: "delta seconds", header: func() string { return "12" }, minimum: 12 * time.Second, maximum: 12 * time.Second, present: true},
		{name: "HTTP date", header: func() string { return time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat) }, minimum: 28 * time.Second, maximum: 31 * time.Second, present: true},
		{name: "invalid", header: func() string { return "later" }},
		{name: "zero", header: func() string { return "0" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
			response.Header.Set("Retry-After", tt.header())
			delay, ok := RetryAfter(retryableHTTPError("contract", response))
			assert.Equal(t, tt.present, ok)
			if tt.present {
				assert.GreaterOrEqual(t, delay, tt.minimum)
				assert.LessOrEqual(t, delay, tt.maximum)
			}
		})
	}
}

func TestMicrosoftGraphProtocolContracts(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		transport  error
		permanent  bool
		wantRetry  bool
		want       string
	}{
		{name: "accepted", status: http.StatusAccepted},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"token-secret"}}`, permanent: true, want: "HTTP 401"},
		{name: "rate limited", status: http.StatusTooManyRequests, retryAfter: "11", wantRetry: true, want: "HTTP 429"},
		{name: "server error", status: http.StatusServiceUnavailable, want: "HTTP 503"},
		{name: "oversized accepted response", status: http.StatusAccepted, body: strings.Repeat("x", maxProtocolResponseBytes+1), permanent: true, want: "exceeds"},
		{name: "network reset", transport: errors.New("connection reset"), want: "connection reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if tt.transport != nil {
					return nil, tt.transport
				}
				header := make(http.Header)
				header.Set("Retry-After", tt.retryAfter)
				return responseWithHeader(request, tt.status, tt.body, header), nil
			})}
			t.Cleanup(client.CloseIdleConnections)
			sender := newMicrosoftSender(map[string]string{
				"client_id": "client", "to": "to@example.com", "access_token": "access-secret", "refresh_token": "refresh-secret",
				"token_type": "Bearer", "token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
			}, client, "", nil, microsoftEndpoints{graphSendURL: "https://graph.invalid/send"})
			err := sender.Send(t.Context(), TextMessage("subject", "body"))
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), "token-secret")
			assert.NotContains(t, err.Error(), "access-secret")
			_, retry := RetryAfter(err)
			assert.Equal(t, tt.wantRetry, retry)
		})
	}
}

func TestMicrosoftRefreshFailuresAreSanitizedAndClassified(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		permanent  bool
		wantRetry  bool
		updateErr  error
		want       string
	}{
		{name: "invalid grant", status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_description":"refresh-secret leaked"}`, permanent: true, want: "invalid_grant"},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":"temporarily_unavailable","error_description":"refresh-secret leaked"}`, retryAfter: "9", wantRetry: true, want: "HTTP 429"},
		{name: "malformed response", status: http.StatusOK, body: `{"access_token":`, want: "refreshing Microsoft token"},
		{name: "persistence failure", status: http.StatusOK, body: `{"access_token":"new","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`, updateErr: errors.New("database unavailable"), want: "persisting refreshed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				header := make(http.Header)
				header.Set("Retry-After", tt.retryAfter)
				return responseWithHeader(request, tt.status, tt.body, header), nil
			})}
			t.Cleanup(client.CloseIdleConnections)
			sender := newMicrosoftSender(map[string]string{
				"client_id": "client", "to": "to@example.com", "access_token": "old", "refresh_token": "refresh-secret",
				"token_expiry": time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
			}, client, "", func(map[string]string) error { return tt.updateErr }, microsoftEndpoints{tokenURL: "https://login.invalid/token", graphSendURL: "https://graph.invalid/send"})
			err := sender.Send(t.Context(), TextMessage("subject", "body"))
			require.Error(t, err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), "refresh-secret")
			_, retry := RetryAfter(err)
			assert.Equal(t, tt.wantRetry, retry)
		})
	}
}

func TestProtocolCancellation(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "robot", run: func(ctx context.Context) error {
			sender := &robotSender{kind: "wecom", webhook: "https://hook.invalid", client: contextBlockingClient()}
			return sender.postJSON(ctx, sender.webhook, map[string]string{"text": "body"})
		}},
		{name: "Graph", run: func(ctx context.Context) error {
			sender := newMicrosoftSender(map[string]string{
				"client_id": "client", "to": "to@example.com", "access_token": "access", "refresh_token": "refresh",
				"token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
			}, contextBlockingClient(), "", nil, microsoftEndpoints{graphSendURL: "https://graph.invalid/send"})
			return sender.Send(ctx, TextMessage("subject", "body"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := tt.run(ctx)
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestFeishuTokenAndImageProtocolFailures(t *testing.T) {
	tests := []struct {
		name       string
		operation  string
		status     int
		body       string
		retryAfter string
		permanent  bool
		wantRetry  bool
		want       string
	}{
		{name: "token rate limit", operation: "token", status: http.StatusTooManyRequests, retryAfter: "13", wantRetry: true, want: "HTTP 429"},
		{name: "token unauthorized", operation: "token", status: http.StatusUnauthorized, permanent: true, want: "HTTP 401"},
		{name: "token malformed", operation: "token", status: http.StatusOK, body: `{"code":`, permanent: true, want: "decoding"},
		{name: "token business error", operation: "token", status: http.StatusOK, body: `{"code":10003,"msg":"app-secret must-not-leak"}`, permanent: true, want: "code=10003"},
		{name: "image server error", operation: "image", status: http.StatusBadGateway, want: "HTTP 502"},
		{name: "image malformed", operation: "image", status: http.StatusOK, body: `{"code":`, permanent: true, want: "decoding"},
		{name: "image business error", operation: "image", status: http.StatusOK, body: `{"code":234,"msg":"app-secret must-not-leak"}`, permanent: true, want: "code=234"},
		{name: "image oversized", operation: "image", status: http.StatusOK, body: strings.Repeat("x", maxProtocolResponseBytes+1), permanent: true, want: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				header := make(http.Header)
				header.Set("Retry-After", tt.retryAfter)
				return responseWithHeader(request, tt.status, tt.body, header), nil
			})}
			t.Cleanup(client.CloseIdleConnections)
			sender := &robotSender{client: client, appID: "app-id", appSecret: "app-secret", feishuTokenCaches: &sync.Map{}}
			var err error
			if tt.operation == "token" {
				_, err = sender.feishuTenantToken(t.Context())
			} else {
				_, err = sender.uploadFeishuImage(t.Context(), "tenant-token", localImage{name: "image.png", contentType: "image/png", data: []byte("png")})
			}
			require.Error(t, err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), "app-secret")
			_, retry := RetryAfter(err)
			assert.Equal(t, tt.wantRetry, retry)
		})
	}
}

func responseWithHeader(request *http.Request, status int, body string, header http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func contextBlockingClient() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
}

func BenchmarkRenderNotificationProtocols(b *testing.B) {
	message := Message{Subject: "benchmark", Sections: []Section{{
		Heading: "dynamic", Paragraphs: []string{strings.Repeat("正文🙂", 100)},
		Images: []Image{{Label: "image", URL: "https://example.invalid/image.png"}},
	}}, Action: Link{Label: "source", URL: "https://t.bilibili.com/1"}}
	benchmarks := []struct {
		name string
		run  func()
	}{
		{name: "plain", run: func() { _ = renderPlainText(message) }},
		{name: "HTML", run: func() { _ = renderHTML(message) }},
		{name: "WeCom Markdown", run: func() { _ = renderMarkdown(message, 4096, true, false) }},
		{name: "DingTalk Markdown", run: func() { _ = renderMarkdown(message, 20_000, false, true) }},
		{name: "Feishu post", run: func() { _ = renderFeishuPayload(message, "1", "signature", nil) }},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				benchmark.run()
			}
		})
	}
}

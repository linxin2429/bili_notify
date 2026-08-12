package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftProgressiveAttachmentProtocols(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		size      int64
		wantRoute string
	}{
		{name: "small attachment", size: 1024, wantRoute: "/me/messages/draft/attachments"},
		{name: "large attachment session", size: 3 << 20, wantRoute: "/me/messages/draft/attachments/createUploadSession"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var routes, ranges []string
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routes = append(routes, r.URL.Path)
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/me/messages":
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":"draft"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/me/messages/draft/attachments":
					var payload map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					assert.Equal(t, "original.bin", payload["name"])
					w.WriteHeader(http.StatusCreated)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createUploadSession"):
					_, _ = fmt.Fprintf(w, `{"uploadUrl":%q}`, server.URL+"/upload")
				case r.Method == http.MethodPut && r.URL.Path == "/upload":
					ranges = append(ranges, r.Header.Get("Content-Range"))
					_, err := io.Copy(io.Discard, r.Body)
					require.NoError(t, err)
					w.WriteHeader(http.StatusCreated)
				case r.Method == http.MethodPost && r.URL.Path == "/me/messages/draft/send":
					w.WriteHeader(http.StatusAccepted)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			dataDir := t.TempDir()
			relative := filepath.ToSlash(filepath.Join("media", "attachment.bin"))
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dataDir, relative)), 0o700))
			file, err := os.Create(filepath.Join(dataDir, relative))
			require.NoError(t, err)
			require.NoError(t, file.Truncate(tt.size))
			require.NoError(t, file.Close())
			sender := newMicrosoftSender(map[string]string{
				"client_id": "client", "to": "to@example.com", "access_token": "access", "refresh_token": "refresh",
				"token_type": "Bearer", "token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
			}, server.Client(), dataDir, nil, microsoftEndpoints{graphSendURL: server.URL + "/me/sendMail"})
			progress, err := sender.SendProgressive(t.Context(), Message{Subject: "subject", Files: []model.DeliveryFile{{ID: "file", Name: "original.bin", LocalPath: relative}}}, nil)
			require.NoError(t, err)
			assert.True(t, progress.TextSent)
			assert.Equal(t, 1, progress.FilesSent)
			assert.Equal(t, "draft", progress.MicrosoftDraftID)
			assert.Contains(t, routes, tt.wantRoute)
			if tt.size >= 3<<20 {
				require.NotEmpty(t, ranges)
				assert.Equal(t, fmt.Sprintf("bytes 0-%d/%d", tt.size-1, tt.size), ranges[0])
			}
		})
	}
}

func TestMicrosoftProgressiveFallsBackToSimpleSendWithoutFiles(t *testing.T) {
	t.Parallel()
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	sender := newMicrosoftSender(map[string]string{
		"client_id": "client", "to": "to@example.com", "access_token": "access", "refresh_token": "refresh",
		"token_type": "Bearer", "token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}, server.Client(), "", nil, microsoftEndpoints{graphSendURL: server.URL})

	progress, err := sender.SendProgressive(t.Context(), TextMessage("subject", "body"), &model.DeliveryProgress{ImagesSent: 2})
	require.NoError(t, err)
	assert.True(t, progress.TextSent)
	assert.Equal(t, 2, progress.ImagesSent)
	graphMessage, ok := received["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "subject", graphMessage["subject"])
}

func TestMicrosoftProgressiveRetryDoesNotRepeatConfirmedDraftWork(t *testing.T) {
	t.Parallel()
	dataDir, relative := writeTestAttachment(t, []byte("attachment"))
	attachmentCalls, sendCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/messages":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"draft"}`)
		case "/me/messages/draft/attachments":
			attachmentCalls++
			if attachmentCalls == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case "/me/messages/draft/send":
			sendCalls++
			if sendCalls == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	sender := newMicrosoftSender(map[string]string{
		"client_id": "client", "to": "to@example.com", "access_token": "access", "refresh_token": "refresh",
		"token_type": "Bearer", "token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}, server.Client(), dataDir, nil, microsoftEndpoints{graphSendURL: server.URL + "/me/sendMail"})
	message := Message{Subject: "subject", Files: []model.DeliveryFile{{ID: "file", Name: "attachment.bin", LocalPath: relative}}}

	progress, err := sender.SendProgressive(t.Context(), message, nil)
	require.Error(t, err)
	assert.True(t, progress.TextSent)
	assert.Zero(t, progress.FilesSent)
	assert.Equal(t, "draft", progress.MicrosoftDraftID)

	progress, err = sender.SendProgressive(t.Context(), message, progress)
	require.Error(t, err)
	assert.Equal(t, 1, progress.FilesSent)
	assert.Equal(t, 2, attachmentCalls)

	progress, err = sender.SendProgressive(t.Context(), message, progress)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.FilesSent)
	assert.Equal(t, 2, attachmentCalls)
	assert.Equal(t, 2, sendCalls)
}

func TestMicrosoftDraftRendersOnlyReadableInlineImages(t *testing.T) {
	t.Parallel()
	dataDir, relative := writeTestAttachment(t, []byte("image-bytes"))
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"draft"}`)
	}))
	t.Cleanup(server.Close)
	sender := newMicrosoftSender(map[string]string{"to": "to@example.com"}, server.Client(), dataDir, nil, microsoftEndpoints{graphSendURL: server.URL + "/sendMail"})
	message := Message{Subject: "subject", Sections: []Section{{Images: []Image{
		{Label: "remote only"},
		{Label: "missing", LocalPath: "media/missing.png"},
		{Label: "local", LocalPath: relative, ContentType: "image/png"},
	}}}}

	draftID, err := sender.createDraft(t.Context(), "token", message)
	require.NoError(t, err)
	assert.Equal(t, "draft", draftID)
	attachments, ok := payload["attachments"].([]any)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	attachment, ok := attachments[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "image/png", attachment["contentType"])
	assert.Equal(t, "image-2", attachment["contentId"])
	assert.Equal(t, true, attachment["isInline"])
}

func TestMicrosoftDraftAndTokenFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) error
		want string
	}{
		{
			name: "missing authorization",
			run: func(t *testing.T) error {
				sender := newMicrosoftSender(map[string]string{"to": "to@example.com"}, http.DefaultClient, "", nil, microsoftEndpoints{})
				_, err := sender.accessToken(t.Context())
				return err
			},
			want: "not authorized",
		},
		{
			name: "refreshed token needs settings updater",
			run: func(t *testing.T) error {
				client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return responseFor(request, http.StatusOK, `{"access_token":"new","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`), nil
				})}
				t.Cleanup(client.CloseIdleConnections)
				sender := newMicrosoftSender(expiredMicrosoftSettings(), client, "", nil, microsoftEndpoints{tokenURL: "https://login.invalid/token"})
				_, err := sender.accessToken(t.Context())
				return err
			},
			want: "settings updater is required",
		},
		{
			name: "settings updater failure",
			run: func(t *testing.T) error {
				client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return responseFor(request, http.StatusOK, `{"access_token":"new","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`), nil
				})}
				t.Cleanup(client.CloseIdleConnections)
				sender := newMicrosoftSender(expiredMicrosoftSettings(), client, "", func(map[string]string) error {
					return errors.New("database unavailable")
				}, microsoftEndpoints{tokenURL: "https://login.invalid/token"})
				_, err := sender.accessToken(t.Context())
				return err
			},
			want: "database unavailable",
		},
		{
			name: "draft response without id",
			run: func(t *testing.T) error {
				client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return responseFor(request, http.StatusCreated, `{}`), nil
				})}
				t.Cleanup(client.CloseIdleConnections)
				sender := newMicrosoftSender(map[string]string{"to": "to@example.com"}, client, "", nil, microsoftEndpoints{graphSendURL: "https://graph.invalid/sendMail"})
				_, err := sender.createDraft(t.Context(), "token", TextMessage("subject", "body"))
				return err
			},
			want: "missing id",
		},
		{
			name: "upload session response without url",
			run: func(t *testing.T) error {
				client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return responseFor(request, http.StatusOK, `{}`), nil
				})}
				t.Cleanup(client.CloseIdleConnections)
				sender := newMicrosoftSender(nil, client, "", nil, microsoftEndpoints{graphSendURL: "https://graph.invalid/sendMail"})
				return sender.addLargeAttachment(t.Context(), "token", "draft", model.DeliveryFile{Name: "large.bin", Size: 3 << 20})
			},
			want: "missing uploadUrl",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run(t)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestMicrosoftDraftGraphProtocolFailures(t *testing.T) {
	tests := []struct {
		name      string
		transport error
		status    int
		body      io.Reader
		target    any
		permanent bool
		want      string
	}{
		{name: "transport", transport: errors.New("connection reset"), want: "connection reset"},
		{name: "response read", status: http.StatusCreated, body: &errorReader{err: errors.New("broken body")}, want: "broken body"},
		{name: "server error", status: http.StatusServiceUnavailable, body: strings.NewReader(`{}`), want: "HTTP 503"},
		{name: "unexpected status", status: http.StatusBadRequest, body: strings.NewReader(`{}`), permanent: true, want: "HTTP 400"},
		{name: "oversized response", status: http.StatusCreated, body: strings.NewReader(strings.Repeat("x", maxProtocolResponseBytes+1)), permanent: true, want: "exceeds"},
		{name: "invalid JSON", status: http.StatusCreated, body: strings.NewReader(`{`), target: &map[string]any{}, permanent: true, want: "decoding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if tt.transport != nil {
					return nil, tt.transport
				}
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(tt.body), Request: request}, nil
			})}
			t.Cleanup(client.CloseIdleConnections)
			sender := &microsoftSender{client: client}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://graph.invalid/messages", nil)
			require.NoError(t, err)
			err = sender.doGraph(t.Context(), request, http.StatusCreated, tt.target)
			require.Error(t, err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func expiredMicrosoftSettings() map[string]string {
	return map[string]string{
		"client_id": "client", "access_token": "old", "refresh_token": "refresh", "token_type": "Bearer",
		"token_expiry": time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
	}
}

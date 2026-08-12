package notify

import (
	"encoding/json"
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

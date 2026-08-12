package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyFilesUsesActualSafeLocalState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    []byte
		localPath  string
		localError string
		minimum    int64
		maximum    int64
		wantReady  int
		wantReason string
	}{
		{name: "ready", content: []byte("hello"), localPath: "ready.bin", minimum: 1, maximum: 5, wantReady: 1},
		{name: "zero", content: []byte{}, localPath: "zero.bin", minimum: 1, wantReason: "文件过小"},
		{name: "over limit", content: []byte("123456"), localPath: "large.bin", minimum: 1, maximum: 5, wantReason: "超过渠道上限"},
		{name: "missing", localPath: "missing.bin", minimum: 1, wantReason: "本地文件不可用"},
		{name: "not localized", localError: "attachment localization failed", minimum: 1, wantReason: "本地化失败"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			relative := ""
			if tt.localPath != "" {
				relative = filepath.ToSlash(filepath.Join("media", tt.localPath))
				if tt.name != "missing" {
					require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dataDir, relative)), 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(dataDir, relative), tt.content, 0o600))
				}
			}
			message := Message{Subject: "files", Files: []model.DeliveryFile{{ID: "id", Name: "original.bin", Size: 999, LocalPath: relative, LocalizeError: tt.localError}}}
			classified, ready := classifyFiles(message, dataDir, tt.minimum, tt.maximum)
			assert.Len(t, ready, tt.wantReady)
			if tt.wantReason == "" {
				assert.Empty(t, classified.Sections)
				assert.Equal(t, int64(len(tt.content)), ready[0].Size)
			} else {
				require.Len(t, classified.Sections, 1)
				assert.Contains(t, classified.Sections[0].Paragraphs[0], tt.wantReason)
			}
		})
	}
}

func TestFeishuSendsApplicationFileAfterBody(t *testing.T) {
	t.Parallel()
	const appID = "file-app"
	cacheKey := feishuTokenCacheKey(appID, "secret")
	feishuTokens.Delete(cacheKey)
	t.Cleanup(func() { feishuTokens.Delete(cacheKey) })
	dataDir, relative := writeTestAttachment(t, []byte("feishu-file"))
	var calls []string
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.Path)
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return responseFor(request, http.StatusOK, `{"code":0,"tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/messages":
			var payload map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			assert.Equal(t, "oc_group", payload["receive_id"])
			if payload["msg_type"] == "post" {
				var post map[string]any
				require.NoError(t, json.Unmarshal([]byte(payload["content"].(string)), &post))
				assert.Contains(t, post, "zh_cn")
				assert.NotContains(t, post, "post")
			}
			return responseFor(request, http.StatusOK, `{"code":0}`), nil
		case "/open-apis/im/v1/files":
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), "feishu-file")
			return responseFor(request, http.StatusOK, `{"code":0,"data":{"file_key":"file-key"}}`), nil
		default:
			return responseFor(request, http.StatusNotFound, `{}`), nil
		}
	})}
	t.Cleanup(client.CloseIdleConnections)
	sender, err := NewSender(model.Channel{Name: "feishu", Type: model.ChannelFeishu, Settings: map[string]string{"app_id": appID, "app_secret": "secret", "chat_id": "oc_group"}}, client, dataDir, nil)
	require.NoError(t, err)
	progress, err := sender.(ProgressiveSender).SendProgressive(t.Context(), Message{Subject: "subject", Files: []model.DeliveryFile{{ID: "file", Name: "original.txt", LocalPath: relative}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.FilesSent)
	assert.Equal(t, []string{"/open-apis/auth/v3/tenant_access_token/internal", "/open-apis/im/v1/messages", "/open-apis/im/v1/files", "/open-apis/im/v1/messages"}, calls)
}

func TestWeComUploadsTemporaryMediaBeforeFileMessage(t *testing.T) {
	t.Parallel()
	dataDir, relative := writeTestAttachment(t, []byte("wecom-file"))
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.URL.Path)
		if request.URL.Path == "/cgi-bin/webhook/upload_media" {
			assert.Equal(t, "robot-key", request.URL.Query().Get("key"))
			assert.Equal(t, "file", request.URL.Query().Get("type"))
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), "wecom-file")
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-id"}`))
			return
		}
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	t.Cleanup(server.Close)
	sender, err := NewSender(model.Channel{Name: "wecom", Type: model.ChannelWeCom, Settings: map[string]string{"webhook": server.URL + "/cgi-bin/webhook/send?key=robot-key"}}, server.Client(), dataDir, nil)
	require.NoError(t, err)
	progress, err := sender.(ProgressiveSender).SendProgressive(t.Context(), Message{Subject: "subject", Files: []model.DeliveryFile{{ID: "file", Name: "original.txt", LocalPath: relative}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.FilesSent)
	assert.Equal(t, []string{"/cgi-bin/webhook/send", "/cgi-bin/webhook/upload_media", "/cgi-bin/webhook/send"}, calls)
}

func writeTestAttachment(t *testing.T, content []byte) (string, string) {
	t.Helper()
	dataDir := t.TempDir()
	relative := filepath.ToSlash(filepath.Join("media", "file.bin"))
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dataDir, relative)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, relative), content, 0o600))
	return dataDir, relative
}

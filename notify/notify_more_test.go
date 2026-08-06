package notify

import (
	"encoding/json"
	"errors"
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
	"golang.org/x/oauth2"
)

func TestFeishuUploadsLocalImages(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	localPath := filepath.Join("media", "42", "dynamic", "image.png")
	absPath := filepath.Join(dataDir, localPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o700))
	require.NoError(t, os.WriteFile(absPath, []byte("image-bytes"), 0o600))

	var paths []string
	var webhookPayload map[string]any
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		body := `{}`
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			body = `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`
		case "/open-apis/im/v1/images":
			assert.Equal(t, "Bearer tenant-token", request.Header.Get("Authorization"))
			assert.Contains(t, request.Header.Get("Content-Type"), "multipart/form-data")
			uploaded, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			assert.Contains(t, string(uploaded), "image-bytes")
			body = `{"code":0,"data":{"image_key":"image-key"}}`
		case "/hook":
			require.NoError(t, json.NewDecoder(request.Body).Decode(&webhookPayload))
			body = `{"code":0}`
		default:
			return responseFor(request, http.StatusNotFound, `{}`), nil
		}
		return responseFor(request, http.StatusOK, body), nil
	})}
	t.Cleanup(client.CloseIdleConnections)
	sender, err := NewSender(model.Channel{
		Name: "feishu", Type: model.ChannelFeishu,
		Settings: map[string]string{
			"webhook": "https://hook.invalid/hook", "secret": "secret",
			"app_id": "contract-app", "app_secret": "app-secret",
		},
	}, client, dataDir, nil)
	require.NoError(t, err)
	message := Message{
		Subject:  "subject",
		Sections: []Section{{Paragraphs: []string{"body"}, Images: []Image{{Label: "image", LocalPath: localPath, ContentType: "image/png"}}}},
	}
	require.NoError(t, sender.Send(t.Context(), message))
	assert.Equal(t, []string{
		"/open-apis/auth/v3/tenant_access_token/internal",
		"/open-apis/im/v1/images",
		"/hook",
	}, paths)
	raw, err := json.Marshal(webhookPayload)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "image-key")
}

func TestMicrosoftDeviceExchange(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_, _ = io.WriteString(w, `{"device_code":"device","user_code":"CODE","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":1}`)
		case "/token":
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "device", r.Form.Get("device_code"))
			_, _ = io.WriteString(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	auth, err := startMicrosoftDeviceAuth(t.Context(), map[string]string{"client_id": "client"}, server.Client(), microsoftEndpoints{
		deviceAuthURL: server.URL + "/device", tokenURL: server.URL + "/token",
	})
	require.NoError(t, err)
	settings, err := auth.Exchange(t.Context(), server.Client())
	require.NoError(t, err)
	assert.Equal(t, "access", settings["access_token"])
	assert.Equal(t, "refresh", settings["refresh_token"])
	assert.Equal(t, "true", settings["authorized"])

	var missing *MicrosoftDeviceAuth
	_, err = missing.Exchange(t.Context(), server.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestMicrosoftHelpersAndFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		permanent bool
	}{
		{name: "invalid grant", err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, permanent: true},
		{name: "interaction required", err: &oauth2.RetrieveError{ErrorCode: "interaction_required"}, permanent: true},
		{name: "temporary", err: errors.New("network unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := classifyMicrosoftTokenError(tt.err)
			assert.Equal(t, tt.permanent, IsPermanent(err))
			assert.ErrorContains(t, err, "refreshing Microsoft token")
		})
	}

	endpoints := microsoftEndpointsFor(map[string]string{"tenant": "organizations"})
	assert.Contains(t, endpoints.deviceAuthURL, "/organizations/")
	assert.Contains(t, endpoints.tokenURL, "/organizations/")
	assert.Equal(t, graphBaseURL+"/me/sendMail", endpoints.graphSendURL)
	defaultEndpoints := microsoftEndpointsFor(nil)
	assert.Contains(t, defaultEndpoints.deviceAuthURL, "/common/")
}

func TestNewSenderVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		channel model.Channel
		wantErr string
	}{
		{name: "email", channel: model.Channel{Name: "email", Type: model.ChannelEmail, Settings: map[string]string{"host": "smtp.example.com", "port": "465", "tls": "tls", "from": "from@example.com", "to": "to@example.com"}}},
		{name: "microsoft", channel: model.Channel{Name: "microsoft", Type: model.ChannelMicrosoft, Settings: map[string]string{"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common", "to": "to@example.com"}}},
		{name: "dingtalk", channel: model.Channel{Name: "dingtalk", Type: model.ChannelDingTalk, Settings: map[string]string{"webhook": "https://example.com/hook", "secret": "secret"}}},
		{name: "feishu", channel: model.Channel{Name: "feishu", Type: model.ChannelFeishu, Settings: map[string]string{"webhook": "https://example.com/hook", "secret": "secret"}}},
		{name: "wecom", channel: model.Channel{Name: "wecom", Type: model.ChannelWeCom, Settings: map[string]string{"webhook": "https://example.com/hook"}}},
		{name: "invalid", channel: model.Channel{Name: "invalid", Type: "unknown"}, wantErr: "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sender, err := NewSender(tt.channel, nil, "", nil)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, sender)
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func responseFor(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestMicrosoftTokenParsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings map[string]string
		wantErr  string
	}{
		{name: "valid", settings: map[string]string{"access_token": "a", "refresh_token": "r", "token_expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}},
		{name: "missing refresh", settings: map[string]string{}, wantErr: "not authorized"},
		{name: "bad expiry", settings: map[string]string{"refresh_token": "r", "token_expiry": "bad"}, wantErr: "invalid Microsoft token expiry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, err := microsoftTokenFromSettings(tt.settings)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "Bearer", token.TokenType)
		})
	}
}

func TestEmailSenderFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings map[string]string
		message  Message
		want     string
	}{
		{name: "invalid port", settings: map[string]string{"port": "invalid"}, want: "parsing SMTP port"},
		{name: "invalid sender", settings: emailTestSettings("bad sender", "to@example.com"), message: TextMessage("subject", "body"), want: "setting sender"},
		{name: "invalid recipient", settings: emailTestSettings("from@example.com", "bad recipient"), message: TextMessage("subject", "body"), want: "setting recipients"},
		{name: "connection failure", settings: emailTestSettings("from@example.com", "to@example.com"), message: Message{
			Subject: "subject", Sections: []Section{{Paragraphs: []string{"body"}, Images: []Image{{Label: "missing", LocalPath: "missing.png"}}}},
		}, want: "sending email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sender, err := newEmailSender(tt.settings, t.TempDir())
			if err != nil {
				assert.ErrorContains(t, err, tt.want)
				return
			}
			err = sender.Send(t.Context(), tt.message)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
			if IsPermanent(err) {
				assert.Error(t, errors.Unwrap(err))
			}
		})
	}
}

func emailTestSettings(from, to string) map[string]string {
	return map[string]string{
		"host": "127.0.0.1", "port": "1", "tls": "tls", "from": from, "to": to,
	}
}

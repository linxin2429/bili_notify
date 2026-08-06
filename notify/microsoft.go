package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"golang.org/x/oauth2"
)

const graphBaseURL = "https://graph.microsoft.com/v1.0"

type microsoftEndpoints struct {
	deviceAuthURL string
	tokenURL      string
	graphSendURL  string
}

func microsoftEndpointsFor(settings map[string]string) microsoftEndpoints {
	tenant := strings.TrimSpace(settings["tenant"])
	if tenant == "" {
		tenant = "common"
	}
	identityBase := "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0"
	return microsoftEndpoints{
		deviceAuthURL: identityBase + "/devicecode",
		tokenURL:      identityBase + "/token",
		graphSendURL:  graphBaseURL + "/me/sendMail",
	}
}

func microsoftOAuthConfig(settings map[string]string, endpoints microsoftEndpoints) oauth2.Config {
	return oauth2.Config{
		ClientID: strings.TrimSpace(settings["client_id"]),
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: endpoints.deviceAuthURL,
			TokenURL:      endpoints.tokenURL,
			AuthStyle:     oauth2.AuthStyleInParams,
		},
		Scopes: []string{"offline_access", "https://graph.microsoft.com/Mail.Send"},
	}
}

type MicrosoftDeviceAuth struct {
	UserCode                string    `json:"user_code"`
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at"`
	Interval                int64     `json:"interval_seconds"`

	config   oauth2.Config
	response oauth2.DeviceAuthResponse
}

func StartMicrosoftDeviceAuth(ctx context.Context, settings map[string]string, client *http.Client) (*MicrosoftDeviceAuth, error) {
	return startMicrosoftDeviceAuth(ctx, settings, oauthClient(client), microsoftEndpointsFor(settings))
}

func startMicrosoftDeviceAuth(ctx context.Context, settings map[string]string, client *http.Client, endpoints microsoftEndpoints) (*MicrosoftDeviceAuth, error) {
	config := microsoftOAuthConfig(settings, endpoints)
	response, err := config.DeviceAuth(oauthContext(ctx, client))
	if err != nil {
		return nil, fmt.Errorf("starting Microsoft device authorization: %w", err)
	}
	return &MicrosoftDeviceAuth{
		UserCode: response.UserCode, VerificationURI: response.VerificationURI,
		VerificationURIComplete: response.VerificationURIComplete,
		ExpiresAt:               response.Expiry, Interval: response.Interval,
		config: config, response: *response,
	}, nil
}

func (a *MicrosoftDeviceAuth) Exchange(ctx context.Context, client *http.Client) (map[string]string, error) {
	if a == nil {
		return nil, errors.New("Microsoft device authorization is required")
	}
	token, err := a.config.DeviceAccessToken(oauthContext(ctx, oauthClient(client)), &a.response)
	if err != nil {
		return nil, fmt.Errorf("completing Microsoft device authorization: %w", err)
	}
	if token.RefreshToken == "" {
		return nil, errors.New("Microsoft did not return a refresh token; ensure offline_access consent was granted")
	}
	return microsoftTokenSettings(token), nil
}

type microsoftSender struct {
	settings       map[string]string
	recipients     []string
	client         *http.Client
	dataDir        string
	updateSettings SettingsUpdater
	endpoints      microsoftEndpoints
}

func newMicrosoftSender(settings map[string]string, client *http.Client, dataDir string, updateSettings SettingsUpdater, endpoints microsoftEndpoints) *microsoftSender {
	recipients := make([]string, 0)
	for recipient := range strings.SplitSeq(settings["to"], ",") {
		address, err := mail.ParseAddress(strings.TrimSpace(recipient))
		if err == nil {
			recipients = append(recipients, address.Address)
		}
	}
	return &microsoftSender{
		settings: settings, recipients: recipients, client: oauthClient(client),
		dataDir: dataDir, updateSettings: updateSettings, endpoints: endpoints,
	}
}

func (s *microsoftSender) Send(ctx context.Context, message Message) error {
	token, err := microsoftTokenFromSettings(s.settings)
	if err != nil {
		return &PermanentError{Err: err}
	}
	config := microsoftOAuthConfig(s.settings, s.endpoints)
	current, err := config.TokenSource(oauthContext(ctx, s.client), token).Token()
	if err != nil {
		return classifyMicrosoftTokenError(err)
	}
	if microsoftTokenChanged(token, current) {
		if s.updateSettings == nil {
			return errors.New("persisting refreshed Microsoft token: settings updater is required")
		}
		if err := s.updateSettings(microsoftTokenSettings(current)); err != nil {
			return fmt.Errorf("persisting refreshed Microsoft token: %w", err)
		}
	}

	recipients := make([]map[string]any, 0, len(s.recipients))
	for _, recipient := range s.recipients {
		recipients = append(recipients, map[string]any{"emailAddress": map[string]string{"address": recipient}})
	}
	attachments := make([]map[string]any, 0)
	htmlBody := renderHTMLWithCID(message, func(image Image, index int) string {
		if image.LocalPath == "" || s.dataDir == "" {
			return ""
		}
		data, contentType, err := media.ReadFile(s.dataDir, image.LocalPath)
		if err != nil || len(data) == 0 {
			return ""
		}
		cid := fmt.Sprintf("image-%d", index)
		name := filepath.Base(image.LocalPath)
		if name == "." || name == "/" || name == "" {
			name = cid
		}
		if contentType == "" {
			contentType = image.ContentType
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		attachments = append(attachments, map[string]any{
			"@odata.type":  "#microsoft.graph.fileAttachment",
			"name":         name,
			"contentType":  contentType,
			"contentBytes": base64.StdEncoding.EncodeToString(data),
			"contentId":    cid,
			"isInline":     true,
		})
		return cid
	})
	graphMessage := map[string]any{
		"subject":      message.Subject,
		"body":         map[string]string{"contentType": "HTML", "content": htmlBody},
		"toRecipients": recipients,
	}
	if len(attachments) > 0 {
		graphMessage["attachments"] = attachments
	}
	payload := map[string]any{
		"message":         graphMessage,
		"saveToSentItems": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: fmt.Errorf("encoding Microsoft Graph message: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoints.graphSendURL, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: fmt.Errorf("creating Microsoft Graph request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+current.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending Microsoft Graph mail: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); err != nil {
		return fmt.Errorf("reading Microsoft Graph response: %w", err)
	}
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}
	graphErr := fmt.Errorf("Microsoft Graph returned HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return graphErr
	}
	return &PermanentError{Err: graphErr}
}

func microsoftTokenFromSettings(settings map[string]string) (*oauth2.Token, error) {
	if settings["refresh_token"] == "" {
		return nil, errors.New("Microsoft channel is not authorized")
	}
	var expiry time.Time
	if value := settings["token_expiry"]; value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, fmt.Errorf("invalid Microsoft token expiry: %w", err)
		}
		expiry = parsed
	}
	tokenType := settings["token_type"]
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return &oauth2.Token{
		AccessToken: settings["access_token"], RefreshToken: settings["refresh_token"],
		TokenType: tokenType, Expiry: expiry,
	}, nil
}

func microsoftTokenSettings(token *oauth2.Token) map[string]string {
	return map[string]string{
		"access_token": token.AccessToken, "refresh_token": token.RefreshToken,
		"token_type": token.TokenType, "token_expiry": token.Expiry.In(time.Local).Format(time.RFC3339Nano),
		"authorized": "true",
	}
}

func microsoftTokenChanged(before, after *oauth2.Token) bool {
	return before.AccessToken != after.AccessToken || before.RefreshToken != after.RefreshToken ||
		before.TokenType != after.TokenType || !before.Expiry.Equal(after.Expiry)
}

func classifyMicrosoftTokenError(err error) error {
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		switch retrieveErr.ErrorCode {
		case "invalid_grant", "invalid_client", "interaction_required", "consent_required":
			return &PermanentError{Err: fmt.Errorf("refreshing Microsoft token: %w", err)}
		}
	}
	return fmt.Errorf("refreshing Microsoft token: %w", err)
}

func oauthClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func oauthContext(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

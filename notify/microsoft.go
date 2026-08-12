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
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
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
		Scopes: []string{"offline_access", "https://graph.microsoft.com/Mail.Send", "https://graph.microsoft.com/Mail.ReadWrite"},
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
		return sanitizedHTTPTransportError("sending Microsoft Graph mail", err)
	}
	defer resp.Body.Close()
	_, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("reading Microsoft Graph response: %w", err)
	}
	if resp.StatusCode == http.StatusAccepted {
		if oversized {
			return &PermanentError{Err: fmt.Errorf("Microsoft Graph response exceeds %d bytes", maxProtocolResponseBytes)}
		}
		return nil
	}
	graphErr := fmt.Errorf("Microsoft Graph returned HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return retryableHTTPError("Microsoft Graph", resp)
	}
	return &PermanentError{Err: graphErr}
}

// SendProgressive uses a persisted draft so file attachment and final-send
// retries do not recreate already confirmed work.
func (s *microsoftSender) SendProgressive(ctx context.Context, message Message, progress *model.DeliveryProgress) (*model.DeliveryProgress, error) {
	current := model.DeliveryProgress{}
	if progress != nil {
		current = *progress
	}
	message, files := classifyFiles(message, s.dataDir, 1, media.MicrosoftMaxFileSize)
	if len(message.Files) == 0 {
		err := s.Send(ctx, message)
		if err == nil {
			current.TextSent = true
		}
		return &current, err
	}
	token, err := s.accessToken(ctx)
	if err != nil {
		return &current, err
	}
	if current.MicrosoftDraftID == "" {
		draftID, err := s.createDraft(ctx, token, message)
		if err != nil {
			return &current, err
		}
		current.MicrosoftDraftID = draftID
		current.TextSent = true
	}
	for index := current.FilesSent; index < len(files); index++ {
		if files[index].Size < 3<<20 {
			err = s.addSmallAttachment(ctx, token, current.MicrosoftDraftID, files[index])
		} else {
			err = s.addLargeAttachment(ctx, token, current.MicrosoftDraftID, files[index])
		}
		if err != nil {
			return &current, err
		}
		current.FilesSent = index + 1
	}
	endpoint := s.graphMessagesURL() + "/" + url.PathEscape(current.MicrosoftDraftID) + "/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return &current, &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if err := s.doGraph(ctx, req, http.StatusAccepted, nil); err != nil {
		return &current, err
	}
	return &current, nil
}

func (s *microsoftSender) accessToken(ctx context.Context) (string, error) {
	token, err := microsoftTokenFromSettings(s.settings)
	if err != nil {
		return "", &PermanentError{Err: err}
	}
	config := microsoftOAuthConfig(s.settings, s.endpoints)
	current, err := config.TokenSource(oauthContext(ctx, s.client), token).Token()
	if err != nil {
		return "", classifyMicrosoftTokenError(err)
	}
	if microsoftTokenChanged(token, current) {
		if s.updateSettings == nil {
			return "", errors.New("persisting refreshed Microsoft token: settings updater is required")
		}
		if err := s.updateSettings(microsoftTokenSettings(current)); err != nil {
			return "", fmt.Errorf("persisting refreshed Microsoft token: %w", err)
		}
	}
	return current.AccessToken, nil
}

func (s *microsoftSender) graphMessagesURL() string {
	if strings.HasSuffix(s.endpoints.graphSendURL, "/me/sendMail") {
		return strings.TrimSuffix(s.endpoints.graphSendURL, "/sendMail") + "/messages"
	}
	return strings.TrimSuffix(s.endpoints.graphSendURL, "/") + "/messages"
}

func (s *microsoftSender) createDraft(ctx context.Context, token string, message Message) (string, error) {
	recipients := make([]map[string]any, 0, len(s.recipients))
	for _, recipient := range s.recipients {
		recipients = append(recipients, map[string]any{"emailAddress": map[string]string{"address": recipient}})
	}
	inline := make([]map[string]any, 0)
	htmlBody := renderHTMLWithCID(message, func(image Image, index int) string {
		if image.LocalPath == "" {
			return ""
		}
		data, detected, err := media.ReadFile(s.dataDir, image.LocalPath)
		if err != nil || len(data) == 0 {
			return ""
		}
		cid := fmt.Sprintf("image-%d", index)
		inline = append(inline, map[string]any{"@odata.type": "#microsoft.graph.fileAttachment", "name": filepath.Base(image.LocalPath),
			"contentType": firstNonEmpty(image.ContentType, detected, "application/octet-stream"), "contentBytes": base64.StdEncoding.EncodeToString(data), "contentId": cid, "isInline": true})
		return cid
	})
	payload := map[string]any{"subject": message.Subject, "body": map[string]string{"contentType": "HTML", "content": htmlBody}, "toRecipients": recipients}
	if len(inline) > 0 {
		payload["attachments"] = inline
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", &PermanentError{Err: fmt.Errorf("encoding Microsoft draft: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.graphMessagesURL(), bytes.NewReader(body))
	if err != nil {
		return "", &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		ID string `json:"id"`
	}
	if err := s.doGraph(ctx, req, http.StatusCreated, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", &PermanentError{Err: errors.New("Microsoft Graph draft response is missing id")}
	}
	return response.ID, nil
}

func (s *microsoftSender) addSmallAttachment(ctx context.Context, token, draftID string, item model.DeliveryFile) error {
	file, _, detected, err := media.OpenFile(s.dataDir, item.LocalPath)
	if err != nil {
		return fmt.Errorf("opening Microsoft attachment %q: %w", item.Name, err)
	}
	data, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("reading Microsoft attachment %q: %w", item.Name, err)
	}
	payload := map[string]any{"@odata.type": "#microsoft.graph.fileAttachment", "name": item.Name,
		"contentType": firstNonEmpty(item.MIME, detected, "application/octet-stream"), "contentBytes": base64.StdEncoding.EncodeToString(data)}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: err}
	}
	endpoint := s.graphMessagesURL() + "/" + url.PathEscape(draftID) + "/attachments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return s.doGraph(ctx, req, http.StatusCreated, nil)
}

func (s *microsoftSender) addLargeAttachment(ctx context.Context, token, draftID string, item model.DeliveryFile) error {
	payload := map[string]any{"AttachmentItem": map[string]any{"attachmentType": "file", "name": item.Name, "size": item.Size}}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: err}
	}
	endpoint := s.graphMessagesURL() + "/" + url.PathEscape(draftID) + "/attachments/createUploadSession"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := s.doGraph(ctx, req, http.StatusOK, &session); err != nil {
		return err
	}
	if session.UploadURL == "" {
		return &PermanentError{Err: errors.New("Microsoft upload session is missing uploadUrl")}
	}
	file, actual, _, err := media.OpenFile(s.dataDir, item.LocalPath)
	if err != nil {
		return fmt.Errorf("opening Microsoft attachment %q: %w", item.Name, err)
	}
	const chunkSize int64 = 12 * 320 * 1024
	for offset := int64(0); offset < actual; {
		length := min(chunkSize, actual-offset)
		chunk := make([]byte, length)
		if _, err := io.ReadFull(file, chunk); err != nil {
			_ = file.Close()
			return fmt.Errorf("reading Microsoft attachment chunk: %w", err)
		}
		put, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(chunk))
		if err != nil {
			_ = file.Close()
			return &PermanentError{Err: err}
		}
		put.Header.Set("Content-Length", strconv.FormatInt(length, 10))
		put.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, actual))
		put.Header.Set("Content-Type", "application/octet-stream")
		expected := http.StatusAccepted
		if offset+length == actual {
			expected = http.StatusCreated
		}
		if err := s.doGraph(ctx, put, expected, nil); err != nil {
			_ = file.Close()
			return err
		}
		offset += length
	}
	_ = file.Close()
	return nil
}

func (s *microsoftSender) doGraph(_ context.Context, req *http.Request, expected int, target any) error {
	resp, err := s.client.Do(req)
	if err != nil {
		return sanitizedHTTPTransportError("Microsoft Graph", err)
	}
	defer resp.Body.Close()
	body, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("reading Microsoft Graph response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return retryableHTTPError("Microsoft Graph", resp)
	}
	if resp.StatusCode != expected {
		return &PermanentError{Err: fmt.Errorf("Microsoft Graph returned HTTP %d", resp.StatusCode)}
	}
	if oversized {
		return &PermanentError{Err: fmt.Errorf("Microsoft Graph response exceeds %d bytes", maxProtocolResponseBytes)}
	}
	if target != nil && len(body) > 0 {
		if err := json.Unmarshal(body, target); err != nil {
			return &PermanentError{Err: fmt.Errorf("decoding Microsoft Graph response: %w", err)}
		}
	}
	return nil
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
		status := 0
		if retrieveErr.Response != nil {
			status = retrieveErr.Response.StatusCode
		}
		safe := fmt.Errorf("refreshing Microsoft token failed: code=%s HTTP=%d", retrieveErr.ErrorCode, status)
		switch retrieveErr.ErrorCode {
		case "invalid_grant", "invalid_client", "interaction_required", "consent_required":
			return &PermanentError{Err: safe}
		}
		if retrieveErr.Response != nil && (retrieveErr.Response.StatusCode == http.StatusTooManyRequests || retrieveErr.Response.StatusCode >= 500) {
			return retryableHTTPError("refreshing Microsoft token", retrieveErr.Response)
		}
		return &PermanentError{Err: safe}
	}
	return fmt.Errorf("refreshing Microsoft token: %w", err)
}

func oauthClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return defaultHTTPClient()
}

func oauthContext(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

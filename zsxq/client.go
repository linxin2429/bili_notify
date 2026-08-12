// Package zsxq implements the server-side Knowledge Planet integration.
package zsxq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/linxin2429/bili_notify/model"
)

const (
	DefaultBaseURL  = "https://mcp.zsxq.com/topic/mcp/"
	maxResponseSize = 8 << 20
)

var (
	ErrAuthentication = errors.New("zsxq authentication failed")
	ErrRateLimited    = errors.New("zsxq request rate limited")
	ErrPermission     = errors.New("zsxq source permission denied")
	ErrRemoteNotFound = errors.New("zsxq remote content not found")
	ErrSchemaDrift    = errors.New("zsxq response schema changed")
	ErrUpstream       = errors.New("zsxq upstream request failed")
)

type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	userAgent  string
	requestID  atomic.Uint64
}

type Option func(*Client) error

func WithBaseURL(raw string) Option {
	return func(client *Client) error {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("zsxq base URL must be absolute")
		}
		client.baseURL = parsed
		return nil
	}
}

func New(httpClient *http.Client, userAgent string, options ...Option) (*Client, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	copy := *httpClient
	copy.Jar = nil
	// The credential is scoped to one configured MCP origin. A redirect is a
	// protocol failure; following it could disclose X-Api-Key to another host.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base, _ := url.Parse(DefaultBaseURL)
	client := &Client{httpClient: &copy, baseURL: base, userAgent: userAgent}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

type User struct {
	ID   string
	Name string
}

// Group is a secret-free summary of a Knowledge Planet group visible to the
// authenticated account.
type Group struct {
	ID        string
	Name      string
	OwnerID   string
	OwnerName string
}

func (c *Client) Groups(ctx context.Context, apiKey string) ([]Group, error) {
	var response struct {
		Groups []struct {
			GroupID json.Number `json:"group_id"`
			Name    string      `json:"name"`
			Owner   struct {
				UserID json.Number `json:"user_id"`
				UID    json.Number `json:"uid"`
				Name   string      `json:"name"`
			} `json:"owner"`
		} `json:"groups"`
	}
	if err := c.doJSON(ctx, apiKey, http.MethodGet, "/v2/groups", nil, nil, &response); err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(response.Groups))
	for _, raw := range response.Groups {
		id := raw.GroupID.String()
		if !decimalID(id) || strings.TrimSpace(raw.Name) == "" {
			return nil, ErrSchemaDrift
		}
		ownerID := raw.Owner.UserID.String()
		if ownerID == "" {
			ownerID = raw.Owner.UID.String()
		}
		if ownerID != "" && !decimalID(ownerID) {
			return nil, ErrSchemaDrift
		}
		groups = append(groups, Group{ID: id, Name: raw.Name, OwnerID: ownerID, OwnerName: raw.Owner.Name})
	}
	return groups, nil
}

func decimalID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (c *Client) Me(ctx context.Context, apiKey string) (User, error) {
	// Knowledge Planet's DWeb /users/self payload uses user.uid. Older fixtures and
	// some nested objects still expose user_id; accept either without guessing names.
	var response struct {
		User struct {
			UID    json.Number `json:"uid"`
			UserID json.Number `json:"user_id"`
			Name   string      `json:"name"`
		} `json:"user"`
	}
	if err := c.doJSON(ctx, apiKey, http.MethodGet, "/v3/users/self", nil, nil, &response); err != nil {
		return User{}, err
	}
	id := response.User.UID.String()
	if id == "" {
		id = response.User.UserID.String()
	}
	if id == "" || response.User.Name == "" {
		return User{}, ErrSchemaDrift
	}
	return User{ID: id, Name: response.User.Name}, nil
}

type TopicPage struct {
	Contents    []model.Content
	Attachments map[string][]model.Attachment
	NextCursor  string
}

// TopicSnapshot is the normalized topic detail plus the comments included in
// that same upstream response. CommentsComplete distinguishes a full small
// thread from the presentation-only prefix returned for larger threads.
type TopicSnapshot struct {
	Content          model.Content
	Attachments      []model.Attachment
	ShownComments    []model.CommentNode
	CommentsComplete bool
}

func (c *Client) Topics(ctx context.Context, apiKey string, source model.Source, cursor string, count int) (TopicPage, error) {
	if source.Platform != model.PlatformZSXQ || source.Type != model.SourceZSXQPlanet {
		return TopicPage{}, errors.New("zsxq source is required")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	query := url.Values{"scope": {"all"}, "count": {strconv.Itoa(count)}}
	if cursor != "" {
		query.Set("end_time", cursor)
	}
	var response struct {
		Topics  []apiTopic `json:"topics"`
		EndTime string     `json:"end_time"`
	}
	if err := c.doJSON(ctx, apiKey, http.MethodGet, "/v2/groups/"+url.PathEscape(source.ExternalID)+"/topics", query, nil, &response); err != nil {
		return TopicPage{}, err
	}
	page := TopicPage{Attachments: make(map[string][]model.Attachment), NextCursor: response.EndTime}
	for _, raw := range response.Topics {
		content, attachments, err := parseTopic(source, raw)
		if err != nil {
			return TopicPage{}, err
		}
		page.Contents = append(page.Contents, content)
		page.Attachments[content.ID] = attachments
	}
	if page.NextCursor == "" && len(response.Topics) > 0 {
		page.NextCursor = cursorBefore(response.Topics[len(response.Topics)-1].CreateTime)
	}
	return page, nil
}

func (c *Client) Topic(ctx context.Context, apiKey string, source model.Source, topicID string) (TopicSnapshot, error) {
	var response struct {
		Topic apiTopic `json:"topic"`
	}
	if err := c.doJSON(ctx, apiKey, http.MethodGet, "/v2/topics/"+url.PathEscape(topicID), nil, nil, &response); err != nil {
		return TopicSnapshot{}, err
	}
	content, attachments, err := parseTopic(source, response.Topic)
	if err != nil {
		return TopicSnapshot{}, err
	}
	comments, complete, err := parseShownComments(content, source.OwnerID, response.Topic.ShowComments, response.Topic.CommentsCount)
	if err != nil {
		return TopicSnapshot{}, err
	}
	return TopicSnapshot{Content: content, Attachments: attachments, ShownComments: comments, CommentsComplete: complete}, nil
}

func (c *Client) populateFileDownloadURLs(ctx context.Context, apiKey string, attachments []model.Attachment) error {
	for index := range attachments {
		attachment := &attachments[index]
		if attachment.Type != model.AttachmentFile || attachment.RemoteURL != "" || attachment.LocalPath != "" {
			continue
		}
		var response struct {
			DownloadURL string `json:"download_url"`
		}
		path := "/v2/files/" + url.PathEscape(attachment.ExternalID) + "/download_url"
		if err := c.doJSON(ctx, apiKey, http.MethodGet, path, nil, nil, &response); err != nil {
			if errors.Is(err, ErrPermission) || errors.Is(err, ErrRemoteNotFound) {
				attachment.LocalizeError = "attachment download unavailable"
				continue
			}
			return err
		}
		parsed, err := url.Parse(response.DownloadURL)
		if err != nil || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") || !parsed.IsAbs() || parsed.Host == "" {
			return ErrSchemaDrift
		}
		attachment.RemoteURL = response.DownloadURL
	}
	return nil
}

type CommentPage struct {
	Nodes      []model.CommentNode
	NextCursor string
}

func (c *Client) Comments(ctx context.Context, apiKey string, content model.Content, ownerID, cursor string, count int) (CommentPage, error) {
	if content.Platform != model.PlatformZSXQ {
		return CommentPage{}, errors.New("zsxq content is required")
	}
	if count <= 0 || count > 100 {
		count = 30
	}
	query := url.Values{"sort": {"asc"}, "count": {strconv.Itoa(count)}, "with_sticky": {"true"}}
	if cursor != "" {
		query.Set("begin_time", cursor)
	}
	var response struct {
		Comments       []apiComment `json:"comments"`
		StickyComments []apiComment `json:"sticky_comments"`
		BeginTime      string       `json:"begin_time"`
	}
	if err := c.doJSON(ctx, apiKey, http.MethodGet, "/v2/topics/"+url.PathEscape(content.ExternalID)+"/comments", query, nil, &response); err != nil {
		return CommentPage{}, err
	}
	page := CommentPage{NextCursor: response.BeginTime}
	comments := response.Comments
	if cursor == "" {
		comments = append(response.StickyComments, comments...)
	}
	seen := make(map[string]struct{})
	for _, raw := range comments {
		if err := appendCommentNodes(&page.Nodes, seen, content, ownerID, raw, "", ""); err != nil {
			return CommentPage{}, err
		}
	}
	if page.NextCursor == "" && len(response.Comments) > 0 {
		page.NextCursor = cursorAfter(response.Comments[len(response.Comments)-1].CreateTime)
	}
	return page, nil
}

func (c *Client) doJSON(ctx context.Context, apiKey, method, path string, query url.Values, input, output any) error {
	arguments := map[string]any{"method": method, "path": path}
	if len(query) != 0 {
		queryObject := make(map[string]any, len(query))
		for key, values := range query {
			if len(values) == 1 {
				queryObject[key] = values[0]
			} else {
				queryObject[key] = values
			}
		}
		arguments["query"] = queryObject
	}
	if input != nil {
		arguments["body"] = input
	}
	id := c.requestID.Add(1)
	encoded, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "call_zsxq_api", "arguments": arguments},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("X-Api-Key", apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: transport", ErrUpstream)
	}
	defer response.Body.Close()
	if err := classifyStatus(response.StatusCode); err != nil {
		return err
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return ErrUpstream
	}
	if len(limited) > maxResponseSize {
		return ErrSchemaDrift
	}
	message, err := decodeMCPMessage(limited, response.Header.Get("Content-Type"), id)
	if err != nil {
		return err
	}
	return decodeMCPResult(message, output)
}

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error json.RawMessage `json:"error"`
}

func decodeMCPMessage(body []byte, contentType string, id uint64) (mcpMessage, error) {
	var candidates [][]byte
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		var eventName string
		var data []string
		flush := func() {
			if eventName == "message" && len(data) != 0 {
				candidates = append(candidates, []byte(strings.Join(data, "\n")))
			}
			eventName, data = "", nil
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
			if line == "" {
				flush()
				continue
			}
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		flush()
	} else {
		candidates = append(candidates, body)
	}
	if len(candidates) != 1 {
		return mcpMessage{}, ErrSchemaDrift
	}
	wantID := strconv.FormatUint(id, 10)
	var message mcpMessage
	if err := decodeStrictJSON(candidates[0], &message); err != nil {
		return mcpMessage{}, ErrSchemaDrift
	}
	if string(message.ID) != wantID || message.JSONRPC != "2.0" {
		return mcpMessage{}, ErrSchemaDrift
	}
	return message, nil
}

func decodeMCPResult(message mcpMessage, output any) error {
	if len(message.Error) != 0 || message.Result == nil || len(message.Result.Content) != 1 || message.Result.Content[0].Type != "text" {
		return ErrUpstream
	}
	text := message.Result.Content[0].Text
	var proxy struct {
		Success    *bool           `json:"success"`
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
		Error      string          `json:"error"`
	}
	if err := decodeStrictJSON([]byte(text), &proxy); err != nil {
		return ErrSchemaDrift
	}
	if proxy.StatusCode != 0 {
		if err := classifyStatus(proxy.StatusCode); err != nil {
			return err
		}
	}
	if proxy.Success == nil {
		return ErrSchemaDrift
	}
	if !*proxy.Success || message.Result.IsError {
		// Authentication failures issued before the API proxy runs do not carry
		// status_code, but the official MCP service includes a structured 401 in
		// its error string. Never return that upstream text to callers.
		if strings.Contains(proxy.Error, `"code":401`) || strings.EqualFold(proxy.Error, "Authentication failed") {
			return ErrAuthentication
		}
		return ErrUpstream
	}
	if proxy.StatusCode == 0 {
		return ErrSchemaDrift
	}
	if len(proxy.Body) == 0 || bytes.Equal(proxy.Body, []byte("null")) {
		return ErrSchemaDrift
	}
	return decodeEnvelope(proxy.Body, output)
}

func decodeStrictJSON(body []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func classifyStatus(status int) error {
	if status == http.StatusUnauthorized {
		return ErrAuthentication
	}
	if status == http.StatusForbidden {
		return ErrPermission
	}
	if status == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if status == http.StatusNotFound {
		return ErrRemoteNotFound
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrUpstream, status)
	}
	return nil
}

func decodeEnvelope(body []byte, output any) error {
	var envelope struct {
		Succeeded *bool           `json:"succeeded"`
		Code      int             `json:"code"`
		Error     string          `json:"error"`
		Info      string          `json:"info"`
		RespData  json.RawMessage `json:"resp_data"`
	}
	if err := decodeStrictJSON(body, &envelope); err != nil {
		return ErrSchemaDrift
	}
	if envelope.Succeeded == nil {
		return ErrSchemaDrift
	}
	if !*envelope.Succeeded {
		return classifyBusinessCode(envelope.Code)
	}
	if output == nil {
		return nil
	}
	if len(envelope.RespData) == 0 || bytes.Equal(envelope.RespData, []byte("null")) {
		return ErrSchemaDrift
	}
	if err := decodeStrictJSON(envelope.RespData, output); err != nil {
		return ErrSchemaDrift
	}
	return nil
}

func classifyBusinessCode(code int) error {
	switch code {
	case 10001, 10002:
		return fmt.Errorf("%w: business code %d", ErrAuthentication, code)
	case 40001:
		return fmt.Errorf("%w: business code %d", ErrRateLimited, code)
	case 403, 1006:
		return fmt.Errorf("%w: business code %d", ErrPermission, code)
	case 404:
		return fmt.Errorf("%w: business code %d", ErrRemoteNotFound, code)
	case 0:
		// Missing/zero code with a failed envelope still means upstream rejected
		// the request; keep the public mapping generic.
		return ErrUpstream
	default:
		return fmt.Errorf("%w: business code %d", ErrUpstream, code)
	}
}

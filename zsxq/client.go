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
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const (
	DefaultBaseURL = "https://api.zsxq.com"
	webVersion     = "2.37.0"
	webUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

var (
	ErrAuthentication    = errors.New("zsxq authentication failed")
	ErrRateLimited       = errors.New("zsxq request rate limited")
	ErrRiskControl       = errors.New("zsxq risk control triggered")
	ErrPermission        = errors.New("zsxq source permission denied")
	ErrUnsupportedClient = errors.New("zsxq rejected non-official client access")
	ErrRemoteNotFound    = errors.New("zsxq remote content not found")
	ErrSchemaDrift       = errors.New("zsxq response schema changed")
	ErrUpstream          = errors.New("zsxq upstream request failed")
)

type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	now        func() time.Time
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

func withNow(now func() time.Time) Option {
	return func(client *Client) error {
		client.now = now
		return nil
	}
}

func New(httpClient *http.Client, options ...Option) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	copy := *httpClient
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base, _ := url.Parse(DefaultBaseURL)
	client := &Client{httpClient: &copy, baseURL: base, now: time.Now}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// Group is a secret-free summary of a Knowledge Planet group visible to the
// authenticated account.
type Group struct {
	ID        string
	Name      string
	OwnerID   string
	OwnerName string
}

func (c *Client) Groups(ctx context.Context, token string) ([]Group, error) {
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
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/groups", nil, nil, &response); err != nil {
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

func (c *Client) Topics(ctx context.Context, token string, source model.Source, cursor string, count int) (TopicPage, error) {
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
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/groups/"+url.PathEscape(source.ExternalID)+"/topics", query, nil, &response); err != nil {
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

func (c *Client) Topic(ctx context.Context, token string, source model.Source, topicID string) (TopicSnapshot, error) {
	var payload json.RawMessage
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/topics/"+url.PathEscape(topicID)+"/info", nil, nil, &payload); err != nil {
		return TopicSnapshot{}, err
	}
	raw, err := decodeTopicDetail(payload)
	if err != nil {
		return TopicSnapshot{}, err
	}
	content, attachments, err := parseTopic(source, raw)
	if err != nil {
		return TopicSnapshot{}, err
	}
	comments, complete, err := parseShownComments(content, source.OwnerID, raw.ShowComments, raw.CommentsCount)
	if err != nil {
		return TopicSnapshot{}, err
	}
	return TopicSnapshot{Content: content, Attachments: attachments, ShownComments: comments, CommentsComplete: complete}, nil
}

func (c *Client) populateFileDownloadURLs(ctx context.Context, token string, attachments []model.Attachment) error {
	for index := range attachments {
		attachment := &attachments[index]
		if attachment.Type != model.AttachmentFile || attachment.RemoteURL != "" || attachment.LocalPath != "" {
			continue
		}
		var response struct {
			DownloadURL string `json:"download_url"`
		}
		path := "/v2/files/" + url.PathEscape(attachment.ExternalID) + "/download_url"
		if err := c.doJSON(ctx, token, http.MethodGet, path, nil, nil, &response); err != nil {
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

func (c *Client) Comments(ctx context.Context, token string, content model.Content, ownerID, cursor string, count int) (CommentPage, error) {
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
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/topics/"+url.PathEscape(content.ExternalID)+"/comments", query, nil, &response); err != nil {
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

func (c *Client) doJSON(ctx context.Context, token, method, path string, query url.Values, input, output any) error {
	target := c.resolveURL(path)
	if query != nil {
		target.RawQuery = query.Encode()
	}
	var body io.Reader
	var encoded []byte
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", webUserAgent)
	request.Header.Set("Referer", "https://wx.zsxq.com/")
	request.Header.Set("X-Timestamp", strconv.FormatInt(c.now().Unix(), 10))
	request.Header.Set("X-Version", webVersion)
	request.AddCookie(&http.Cookie{Name: AccessTokenKey, Value: token})
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrUpstream)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		location := response.Header.Get("Location")
		if parsed, parseErr := url.Parse(location); parseErr == nil && strings.EqualFold(parsed.Hostname(), "wx.zsxq.com") && strings.Contains(parsed.Path, "login") {
			return ErrAuthentication
		}
	}
	if err := classifyStatus(response.StatusCode); err != nil {
		return err
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return ErrUpstream
	}
	if len(limited) > 8<<20 {
		return ErrSchemaDrift
	}
	return decodeEnvelope(limited, output)
}

func decodeTopicDetail(payload json.RawMessage) (apiTopic, error) {
	var wrapped struct {
		Topic json.RawMessage `json:"topic"`
	}
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		return apiTopic{}, ErrSchemaDrift
	}
	raw := payload
	if len(wrapped.Topic) != 0 && !bytes.Equal(wrapped.Topic, []byte("null")) {
		raw = wrapped.Topic
	}
	var topic apiTopic
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&topic); err != nil {
		return apiTopic{}, ErrSchemaDrift
	}
	if topic.TopicID.String() == "" {
		return apiTopic{}, ErrSchemaDrift
	}
	return topic, nil
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
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
	decoder = json.NewDecoder(bytes.NewReader(envelope.RespData))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return ErrSchemaDrift
	}
	return nil
}

func classifyBusinessCode(code int) error {
	switch code {
	case 10001, 10002:
		return fmt.Errorf("%w: business code %d", ErrAuthentication, code)
	case 10003:
		return fmt.Errorf("%w: signature rejected", ErrUpstream)
	case 40001:
		return fmt.Errorf("%w: business code %d", ErrRateLimited, code)
	case 403, 1006:
		return fmt.Errorf("%w: business code %d", ErrPermission, code)
	case 1059:
		return fmt.Errorf("%w: business code %d", ErrUnsupportedClient, code)
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

func (c *Client) resolveURL(path string) url.URL {
	target := *c.baseURL
	target.Path = strings.TrimSuffix(c.baseURL.Path, "/") + path
	return target
}

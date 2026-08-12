// Package zsxq implements the server-side Knowledge Planet integration.
package zsxq

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
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
	appVersion     = "2.83.0"
	signingSecret  = "zsxq-sdk-secret"
)

var (
	ErrAuthentication = errors.New("zsxq authentication failed")
	ErrRateLimited    = errors.New("zsxq request rate limited")
	ErrRiskControl    = errors.New("zsxq risk control triggered")
	ErrPermission     = errors.New("zsxq source permission denied")
	ErrRemoteNotFound = errors.New("zsxq remote content not found")
	ErrSchemaDrift    = errors.New("zsxq response schema changed")
	ErrUpstream       = errors.New("zsxq upstream request failed")
)

type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	userAgent  string
	adUID      string
	now        func() time.Time
	requestID  func() string
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

func withProtocolValues(now func() time.Time, requestID func() string, adUID string) Option {
	return func(client *Client) error {
		client.now, client.requestID, client.adUID = now, requestID, adUID
		return nil
	}
}

func New(httpClient *http.Client, userAgent string, options ...Option) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	copy := *httpClient
	copy.Jar = nil
	base, _ := url.Parse(DefaultBaseURL)
	client := &Client{httpClient: &copy, baseURL: base, userAgent: userAgent, adUID: newRequestID(), now: time.Now, requestID: newRequestID}
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

func (c *Client) Me(ctx context.Context, token string) (User, error) {
	// Knowledge Planet's DWeb /users/self payload uses user.uid. Older fixtures and
	// some nested objects still expose user_id; accept either without guessing names.
	var response struct {
		User struct {
			UID    json.Number `json:"uid"`
			UserID json.Number `json:"user_id"`
			Name   string      `json:"name"`
		} `json:"user"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, "/v3/users/self", nil, nil, &response); err != nil {
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
			Owner   apiUser     `json:"owner"`
		} `json:"groups"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/groups", nil, nil, &response); err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(response.Groups))
	for _, item := range response.Groups {
		if item.GroupID.String() == "" || item.Name == "" || item.Owner.UserID.String() == "" {
			return nil, ErrSchemaDrift
		}
		groups = append(groups, Group{ID: item.GroupID.String(), Name: item.Name, OwnerID: item.Owner.UserID.String(), OwnerName: item.Owner.Name})
	}
	return groups, nil
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
	var response struct {
		Topic apiTopic `json:"topic"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, "/v2/topics/"+url.PathEscape(topicID), nil, nil, &response); err != nil {
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
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Origin", "https://wx.zsxq.com")
	request.Header.Set("Referer", "https://wx.zsxq.com/")
	request.Header.Set("Authorization", token)
	c.sign(request, encoded)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrUpstream)
	}
	defer response.Body.Close()
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

func (c *Client) sign(request *http.Request, body []byte) {
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	requestID := c.requestID()
	plain := timestamp + "\n" + strings.ToUpper(request.Method) + "\n" + request.URL.Path
	if len(body) != 0 {
		plain += "\n" + string(body)
	}
	digest := hmac.New(sha1.New, []byte(signingSecret))
	_, _ = digest.Write([]byte(plain))
	request.Header.Set("X-Request-Id", requestID)
	request.Header.Set("X-Version", appVersion)
	request.Header.Set("X-Signature", hex.EncodeToString(digest.Sum(nil)))
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Aduid", c.adUID)
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

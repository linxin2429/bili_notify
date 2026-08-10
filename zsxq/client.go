// Package zsxq implements the server-side Knowledge Planet integration. The
// client owns a cookie jar and never exposes upstream response bodies in errors.
package zsxq

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const DefaultBaseURL = "https://api.zsxq.com/v2"

var (
	ErrAuthentication = errors.New("zsxq authentication failed")
	ErrRateLimited    = errors.New("zsxq request rate limited")
	ErrRiskControl    = errors.New("zsxq risk control triggered")
	ErrRemoteNotFound = errors.New("zsxq remote content not found")
	ErrSchemaDrift    = errors.New("zsxq response schema changed")
	ErrUpstream       = errors.New("zsxq upstream request failed")
	ErrInvalidPhone   = errors.New("zsxq phone number is invalid")
	ErrPhoneUnbound   = errors.New("zsxq phone number is not bound")
)

type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	userAgent  string
	adUID      string
	login      *loginCipher
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

func New(httpClient *http.Client, userAgent string, options ...Option) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	copy := *httpClient
	if copy.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		copy.Jar = jar
	}
	base, _ := url.Parse(DefaultBaseURL)
	login, err := newLoginCipher()
	if err != nil {
		return nil, fmt.Errorf("initializing ZSXQ login encryption: %w", err)
	}
	client := &Client{httpClient: &copy, baseURL: base, userAgent: userAgent, adUID: newRequestID(), login: login, now: time.Now}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (c *Client) SetSession(cookies map[string]string) {
	values := make([]*http.Cookie, 0, len(cookies))
	for name, value := range cookies {
		values = append(values, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true})
	}
	c.httpClient.Jar.SetCookies(c.baseURL, values)
}

func (c *Client) Session() map[string]string {
	result := make(map[string]string)
	for _, cookie := range c.httpClient.Jar.Cookies(c.baseURL) {
		result[cookie.Name] = cookie.Value
	}
	return result
}

func (c *Client) ClearSession() {
	for _, cookie := range c.httpClient.Jar.Cookies(c.baseURL) {
		cookie.Value = ""
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
		c.httpClient.Jar.SetCookies(c.baseURL, []*http.Cookie{cookie})
	}
}

type SMSCodeRequest struct {
	CountryCode        string `json:"country_code"`
	Phone              string `json:"phone"`
	CaptchaVerifyParam string `json:"captcha_verify_param"`
	AgreementAccepted  bool   `json:"agreement_accepted"`
}

func (request SMSCodeRequest) Validate() error {
	if !request.AgreementAccepted {
		return errors.New("user agreement and privacy policy must be accepted")
	}
	if !strings.HasPrefix(request.CountryCode, "+") || len(request.CountryCode) < 2 || len(request.CountryCode) > 5 {
		return errors.New("country_code must start with +")
	}
	phone := strings.TrimSpace(request.Phone)
	if len(phone) < 5 || len(phone) > 20 {
		return errors.New("phone number length is invalid")
	}
	for _, character := range phone {
		if character < '0' || character > '9' {
			return errors.New("phone must contain digits only")
		}
	}
	if strings.TrimSpace(request.CaptchaVerifyParam) == "" {
		return errors.New("captcha verification is required")
	}
	return nil
}

// SendSMSCode forwards the one-time Alibaba CAPTCHA result. The request value
// is never retained by Client and upstream bodies are deliberately redacted.
func (c *Client) SendSMSCode(ctx context.Context, request SMSCodeRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	payload := map[string]any{"req_data": map[string]any{
		"phone": map[string]any{
			"country_code": strings.TrimPrefix(request.CountryCode, "+"),
			"phone_number": request.Phone,
		},
		"captcha_v2": map[string]any{"captcha_verify_param": request.CaptchaVerifyParam},
	}}
	return c.doJSON(ctx, http.MethodPost, "/verify_codes", nil, payload, nil)
}

type LoginResult struct {
	AccountID   string
	AccountName string
	Cookies     map[string]string
}

func (c *Client) Login(ctx context.Context, countryCode, phone, code string) (LoginResult, error) {
	if strings.TrimSpace(code) == "" {
		return LoginResult{}, errors.New("SMS code is required")
	}
	payload := map[string]any{"req_data": map[string]any{"client": "DWeb", "phone": map[string]any{
		"country_code": strings.TrimPrefix(countryCode, "+"), "phone_number": phone, "verify_code": code,
	}}}
	if err := c.doEncryptedLogin(ctx, payload); err != nil {
		return LoginResult{}, err
	}
	account, err := c.Me(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{AccountID: account.ID, AccountName: account.Name, Cookies: c.Session()}, nil
}

type User struct {
	ID   string
	Name string
}

func (c *Client) Me(ctx context.Context) (User, error) {
	var response struct {
		User struct {
			UserID json.Number `json:"user_id"`
			Name   string      `json:"name"`
		} `json:"user"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/users/self", nil, nil, &response); err != nil {
		return User{}, err
	}
	if response.User.UserID.String() == "" || response.User.Name == "" {
		return User{}, ErrSchemaDrift
	}
	return User{ID: response.User.UserID.String(), Name: response.User.Name}, nil
}

type Group struct {
	ID        string
	Name      string
	OwnerID   string
	OwnerName string
}

func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	var response struct {
		Groups []struct {
			GroupID json.Number `json:"group_id"`
			Name    string      `json:"name"`
			Owner   apiUser     `json:"owner"`
		} `json:"groups"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/groups", nil, nil, &response); err != nil {
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

func (c *Client) Topics(ctx context.Context, source model.Source, cursor string, count int) (TopicPage, error) {
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
	if err := c.doJSON(ctx, http.MethodGet, "/groups/"+url.PathEscape(source.ExternalID)+"/topics", query, nil, &response); err != nil {
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

func (c *Client) Topic(ctx context.Context, source model.Source, topicID string) (model.Content, []model.Attachment, error) {
	var response struct {
		Topic apiTopic `json:"topic"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/topics/"+url.PathEscape(topicID), nil, nil, &response); err != nil {
		return model.Content{}, nil, err
	}
	content, attachments, err := parseTopic(source, response.Topic)
	return content, attachments, err
}

type CommentPage struct {
	Nodes      []model.CommentNode
	NextCursor string
}

func (c *Client) Comments(ctx context.Context, content model.Content, ownerID, cursor string, count int) (CommentPage, error) {
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
	if err := c.doJSON(ctx, http.MethodGet, "/topics/"+url.PathEscape(content.ExternalID)+"/comments", query, nil, &response); err != nil {
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

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, input, output any) error {
	target := c.resolveURL(path)
	if query != nil {
		target.RawQuery = query.Encode()
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
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
	c.sign(request)
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
	limited, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return ErrUpstream
	}
	return decodeEnvelope(limited, output)
}

func classifyStatus(status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrAuthentication
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
		Succeeded bool            `json:"succeeded"`
		Code      int             `json:"code"`
		RespData  json.RawMessage `json:"resp_data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return ErrSchemaDrift
	}
	if !envelope.Succeeded {
		switch envelope.Code {
		case 401, 1059:
			return ErrAuthentication
		case 1004:
			return ErrInvalidPhone
		case 1031, 10013:
			return ErrPhoneUnbound
		case 429:
			return ErrRateLimited
		case 403, 1006:
			return ErrRiskControl
		default:
			return ErrUpstream
		}
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

func (c *Client) doEncryptedLogin(ctx context.Context, input any) error {
	plain, err := json.Marshal(input)
	if err != nil {
		return err
	}
	encrypted, err := c.login.encrypt(plain)
	if err != nil {
		return err
	}
	target := c.resolveURL("/access_tokens")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(encrypted))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/plain")
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Origin", "https://wx.zsxq.com")
	request.Header.Set("Referer", "https://wx.zsxq.com/")
	key, iv := c.login.headers()
	request.Header.Set("X-Key", key)
	request.Header.Set("X-IV", iv)
	c.sign(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrUpstream)
	}
	defer response.Body.Close()
	if err := classifyStatus(response.StatusCode); err != nil {
		return err
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return ErrUpstream
	}
	decrypted, err := c.login.decrypt(strings.TrimSpace(string(encoded)))
	if err != nil {
		return err
	}
	return decodeEnvelope(decrypted, nil)
}

func (c *Client) resolveURL(path string) url.URL {
	target := *c.baseURL
	basePath := strings.TrimSuffix(c.baseURL.Path, "/")
	if isV3Path(path) && strings.HasSuffix(basePath, "/v2") {
		basePath = strings.TrimSuffix(basePath, "/v2") + "/v3"
	}
	target.Path = basePath + path
	return target
}

func isV3Path(path string) bool {
	switch path {
	case "/access_tokens", "/verify_codes", "/users/self", "/users/self/login_info", "/users/self/accounts":
		return true
	default:
		return false
	}
}

func (c *Client) sign(request *http.Request) {
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	requestID := newRequestID()
	digest := sha256.Sum256([]byte(request.URL.String() + " " + timestamp + " " + requestID))
	request.Header.Set("X-Request-Id", requestID)
	request.Header.Set("X-Version", map[bool]string{true: "3.20.0", false: "2.95.0"}[strings.Contains(request.URL.Path, "/v3/")])
	request.Header.Set("X-Signature", hex.EncodeToString(digest[:]))
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

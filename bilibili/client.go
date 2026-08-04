package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const (
	defaultAPI      = "https://api.bilibili.com"
	defaultPassport = "https://passport.bilibili.com"
)

type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorRiskControl    ErrorKind = "risk_control"
	ErrorSchema         ErrorKind = "schema"
	ErrorTemporary      ErrorKind = "temporary"
)

type APIError struct {
	Kind       ErrorKind
	HTTPStatus int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bilibili %s error (http=%d code=%d): %s", e.Kind, e.HTTPStatus, e.Code, e.Message)
}

func IsAuthentication(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Kind == ErrorAuthentication
}

func IsRiskControl(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Kind == ErrorRiskControl
}

type Client struct {
	httpClient  *http.Client
	apiURL      string
	passportURL string
	userAgent   string
	mu          sync.RWMutex
	cookies     map[string]string
}

type Option func(*Client)

func WithBaseURLs(apiURL, passportURL string) Option {
	return func(c *Client) {
		c.apiURL = strings.TrimRight(apiURL, "/")
		c.passportURL = strings.TrimRight(passportURL, "/")
	}
}

func New(httpClient *http.Client, userAgent string, opts ...Option) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	c := &Client{
		httpClient:  httpClient,
		apiURL:      defaultAPI,
		passportURL: defaultPassport,
		userAgent:   userAgent,
		cookies:     make(map[string]string),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) SetSession(session model.BiliSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookies = make(map[string]string, len(session.Cookies))
	for k, v := range session.Cookies {
		c.cookies[k] = v
	}
}

func (c *Client) ClearSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.cookies)
}

func (c *Client) addHeaders(req *http.Request, withAuth bool) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if !withAuth {
		return
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	parts := make([]string, 0, len(c.cookies))
	for name, value := range c.cookies {
		parts = append(parts, name+"="+value)
	}
	if len(parts) > 0 {
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}
}

func (c *Client) get(ctx context.Context, endpoint string, query url.Values, withAuth bool) (*http.Response, []byte, error) {
	u := endpoint
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("creating request: %w", err)
	}
	c.addHeaders(req, withAuth)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("requesting bilibili: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp, nil, fmt.Errorf("reading bilibili response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp, body, &APIError{Kind: ErrorAuthentication, HTTPStatus: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPreconditionFailed {
		return resp, body, &APIError{Kind: ErrorRiskControl, HTTPStatus: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, body, &APIError{Kind: ErrorTemporary, HTTPStatus: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return resp, body, nil
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeEnvelope(body []byte, dst any) error {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return &APIError{Kind: ErrorSchema, Message: "invalid JSON envelope: " + err.Error()}
	}
	if env.Code != 0 {
		kind := ErrorTemporary
		switch env.Code {
		case -101, -111:
			kind = ErrorAuthentication
		case -352, -412:
			kind = ErrorRiskControl
		}
		return &APIError{Kind: kind, Code: env.Code, Message: env.Message}
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return &APIError{Kind: ErrorSchema, Message: "invalid data object: " + err.Error()}
	}
	return nil
}

type QRLogin struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

func (c *Client) GenerateQR(ctx context.Context) (QRLogin, error) {
	_, body, err := c.get(ctx, c.passportURL+"/x/passport-login/web/qrcode/generate", nil, false)
	if err != nil {
		return QRLogin{}, err
	}
	var data struct {
		URL string `json:"url"`
		Key string `json:"qrcode_key"`
	}
	if err := decodeEnvelope(body, &data); err != nil {
		return QRLogin{}, err
	}
	if data.URL == "" || data.Key == "" {
		return QRLogin{}, &APIError{Kind: ErrorSchema, Message: "QR response is missing url or key"}
	}
	return QRLogin{Key: data.Key, URL: data.URL}, nil
}

type QRStatus string

const (
	QRWaiting QRStatus = "waiting"
	QRScanned QRStatus = "scanned"
	QRExpired QRStatus = "expired"
	QRSuccess QRStatus = "success"
)

func (c *Client) PollQR(ctx context.Context, key string) (QRStatus, model.BiliSession, error) {
	resp, body, err := c.get(ctx, c.passportURL+"/x/passport-login/web/qrcode/poll", url.Values{"qrcode_key": {key}}, false)
	if err != nil {
		return "", model.BiliSession{}, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", model.BiliSession{}, &APIError{Kind: ErrorSchema, Message: "invalid QR poll envelope"}
	}
	if env.Code != 0 {
		return "", model.BiliSession{}, &APIError{Kind: ErrorTemporary, Code: env.Code, Message: env.Message}
	}
	var data struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return "", model.BiliSession{}, &APIError{Kind: ErrorSchema, Message: "invalid QR poll data"}
	}
	switch data.Code {
	case 86101:
		return QRWaiting, model.BiliSession{}, nil
	case 86090:
		return QRScanned, model.BiliSession{}, nil
	case 86038:
		return QRExpired, model.BiliSession{}, nil
	case 0:
		cookies := make(map[string]string)
		for _, cookie := range resp.Cookies() {
			if cookie.Value != "" {
				cookies[cookie.Name] = cookie.Value
			}
		}
		if cookies["SESSDATA"] == "" {
			return "", model.BiliSession{}, &APIError{Kind: ErrorSchema, Message: "successful login did not return SESSDATA"}
		}
		return QRSuccess, model.BiliSession{Cookies: cookies, UpdatedAt: time.Now().UTC()}, nil
	default:
		return "", model.BiliSession{}, &APIError{Kind: ErrorSchema, Code: data.Code, Message: "unknown QR login state"}
	}
}

func (c *Client) ValidateSession(ctx context.Context) (string, error) {
	_, body, err := c.get(ctx, c.apiURL+"/x/web-interface/nav", nil, true)
	if err != nil {
		return "", err
	}
	var data struct {
		IsLogin bool   `json:"isLogin"`
		Name    string `json:"uname"`
	}
	if err := decodeEnvelope(body, &data); err != nil {
		return "", err
	}
	if !data.IsLogin {
		return "", &APIError{Kind: ErrorAuthentication, Message: "session is not logged in"}
	}
	return data.Name, nil
}

type Page struct {
	Items   []model.Dynamic
	Offset  string
	HasMore bool
	UPName  string
}

func (c *Client) FetchPage(ctx context.Context, uid, offset string) (Page, error) {
	query := url.Values{"host_mid": {uid}}
	if offset != "" {
		query.Set("offset", offset)
	}
	_, body, err := c.get(ctx, c.apiURL+"/x/polymer/web-dynamic/v1/feed/space", query, true)
	if err != nil {
		return Page{}, err
	}
	var data struct {
		HasMore bool              `json:"has_more"`
		Offset  string            `json:"offset"`
		Items   []json.RawMessage `json:"items"`
	}
	if err := decodeEnvelope(body, &data); err != nil {
		return Page{}, err
	}
	page := Page{Offset: data.Offset, HasMore: data.HasMore}
	for _, raw := range data.Items {
		dynamic, upName, err := parseDynamic(uid, raw)
		if err != nil {
			return Page{}, err
		}
		if dynamic.Type == "DYNAMIC_TYPE_LIVE_RCMD" {
			continue
		}
		if page.UPName == "" {
			page.UPName = upName
		}
		page.Items = append(page.Items, dynamic)
	}
	return page, nil
}

var supportedDynamicTypes = map[string]bool{
	"DYNAMIC_TYPE_WORD":          true,
	"DYNAMIC_TYPE_DRAW":          true,
	"DYNAMIC_TYPE_AV":            true,
	"DYNAMIC_TYPE_ARTICLE":       true,
	"DYNAMIC_TYPE_FORWARD":       true,
	"DYNAMIC_TYPE_PGC":           true,
	"DYNAMIC_TYPE_COMMON_SQUARE": true,
	"DYNAMIC_TYPE_LIVE_RCMD":     true,
}

func parseDynamic(uid string, raw json.RawMessage) (model.Dynamic, string, error) {
	var item struct {
		ID      string `json:"id_str"`
		Type    string `json:"type"`
		Modules struct {
			Author struct {
				Name  string `json:"name"`
				PubTS int64  `json:"pub_ts"`
			} `json:"module_author"`
			Dynamic struct {
				Desc *struct {
					Text string `json:"text"`
				} `json:"desc"`
				Major json.RawMessage `json:"major"`
			} `json:"module_dynamic"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return model.Dynamic{}, "", &APIError{Kind: ErrorSchema, Message: "invalid dynamic item: " + err.Error()}
	}
	if item.ID == "" || item.Type == "" || item.Modules.Author.PubTS == 0 {
		return model.Dynamic{}, "", &APIError{Kind: ErrorSchema, Message: "dynamic item is missing id, type, or publication time"}
	}
	if !supportedDynamicTypes[item.Type] {
		return model.Dynamic{}, "", &APIError{Kind: ErrorSchema, Message: "unsupported dynamic type " + item.Type}
	}
	parts := make([]string, 0, 3)
	if item.Modules.Dynamic.Desc != nil {
		parts = appendNonEmpty(parts, item.Modules.Dynamic.Desc.Text)
	}
	parts = appendNonEmpty(parts, majorSummary(item.Modules.Dynamic.Major))
	summary := truncate(strings.Join(parts, "\n"), 500)
	return model.Dynamic{
		ID:          item.ID,
		UID:         uid,
		UPName:      item.Modules.Author.Name,
		Type:        item.Type,
		PublishedAt: time.Unix(item.Modules.Author.PubTS, 0).UTC(),
		Summary:     summary,
		URL:         "https://t.bilibili.com/" + item.ID,
	}, item.Modules.Author.Name, nil
}

func majorSummary(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var major map[string]json.RawMessage
	if json.Unmarshal(raw, &major) != nil {
		return ""
	}
	for _, key := range []string{"archive", "article", "pgc", "common", "opus"} {
		childRaw, ok := major[key]
		if !ok {
			continue
		}
		var child struct {
			Title   string `json:"title"`
			Desc    string `json:"desc"`
			Summary struct {
				Text string `json:"text"`
			} `json:"summary"`
		}
		if json.Unmarshal(childRaw, &child) != nil {
			continue
		}
		return strings.TrimSpace(strings.Join([]string{child.Title, child.Desc, child.Summary.Text}, "\n"))
	}
	return ""
}

func appendNonEmpty(parts []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return parts
	}
	return append(parts, value)
}

func truncate(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func ParseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return time.Duration(seconds) * time.Second
}

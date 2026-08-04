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

type unixTimestamp int64

func (t *unixTimestamp) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("decoding quoted Unix timestamp: %w", err)
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parsing Unix timestamp %q: %w", value, err)
	}
	*t = unixTimestamp(parsed)
	return nil
}

// flexibleInt accepts bare or quoted JSON integers. Bilibili sometimes serializes
// media dimensions as strings (e.g. "1080") instead of numbers.
type flexibleInt int

func (v *flexibleInt) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "null" || value == "" {
		*v = 0
		return nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("decoding quoted integer: %w", err)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			*v = 0
			return nil
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parsing integer %q: %w", value, err)
	}
	*v = flexibleInt(parsed)
	return nil
}

type displayText string

func (t *displayText) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "null" {
		*t = ""
		return nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return fmt.Errorf("decoding display text: %w", err)
		}
		*t = displayText(decoded)
		return nil
	}
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return fmt.Errorf("parsing display number %q: %w", value, err)
	}
	*t = displayText(value)
	return nil
}

type rawRichTextNode struct {
	OrigText string `json:"orig_text"`
	Text     string `json:"text"`
	JumpURL  string `json:"jump_url"`
}

type rawDynamic struct {
	ID      string          `json:"id_str"`
	Type    string          `json:"type"`
	Orig    json.RawMessage `json:"orig"`
	Modules struct {
		Author struct {
			MID   json.RawMessage `json:"mid"`
			Name  string          `json:"name"`
			PubTS unixTimestamp   `json:"pub_ts"`
		} `json:"module_author"`
		Dynamic struct {
			Desc *struct {
				Text          string            `json:"text"`
				RichTextNodes []rawRichTextNode `json:"rich_text_nodes"`
			} `json:"desc"`
			Major json.RawMessage `json:"major"`
		} `json:"module_dynamic"`
		Stat *struct {
			Forward struct {
				Count int64 `json:"count"`
			} `json:"forward"`
			Comment struct {
				Count int64 `json:"count"`
			} `json:"comment"`
			Like struct {
				Count int64 `json:"count"`
			} `json:"like"`
		} `json:"module_stat"`
	} `json:"modules"`
}

type rawMajor struct {
	Archive *struct {
		Title        string `json:"title"`
		Desc         string `json:"desc"`
		Cover        string `json:"cover"`
		JumpURL      string `json:"jump_url"`
		DurationText string `json:"duration_text"`
		Badge        struct {
			Text string `json:"text"`
		} `json:"badge"`
		Stat struct {
			Play    displayText `json:"play"`
			Danmaku displayText `json:"danmaku"`
		} `json:"stat"`
	} `json:"archive"`
	Draw *struct {
		Items []struct {
			Src    string      `json:"src"`
			Width  flexibleInt `json:"width"`
			Height flexibleInt `json:"height"`
		} `json:"items"`
	} `json:"draw"`
	Article *struct {
		Title   string   `json:"title"`
		Desc    string   `json:"desc"`
		Covers  []string `json:"covers"`
		JumpURL string   `json:"jump_url"`
		Label   string   `json:"label"`
	} `json:"article"`
	PGC *struct {
		Title   string `json:"title"`
		Cover   string `json:"cover"`
		JumpURL string `json:"jump_url"`
		Badge   struct {
			Text string `json:"text"`
		} `json:"badge"`
	} `json:"pgc"`
	Common *struct {
		Title   string `json:"title"`
		Desc    string `json:"desc"`
		Cover   string `json:"cover"`
		JumpURL string `json:"jump_url"`
		Badge   struct {
			Text string `json:"text"`
		} `json:"badge"`
	} `json:"common"`
	Opus *struct {
		Title   string `json:"title"`
		JumpURL string `json:"jump_url"`
		Summary struct {
			Text          string            `json:"text"`
			RichTextNodes []rawRichTextNode `json:"rich_text_nodes"`
		} `json:"summary"`
		Pics []struct {
			URL    string      `json:"url"`
			Src    string      `json:"src"`
			Width  flexibleInt `json:"width"`
			Height flexibleInt `json:"height"`
		} `json:"pics"`
	} `json:"opus"`
}

func parseDynamic(uid string, raw json.RawMessage) (model.Dynamic, string, error) {
	dynamic, err := parseDynamicItem(uid, raw)
	if err != nil {
		return model.Dynamic{}, "", err
	}
	return dynamic, dynamic.UPName, nil
}

func parseDynamicItem(uid string, raw json.RawMessage) (model.Dynamic, error) {
	var item rawDynamic
	if err := json.Unmarshal(raw, &item); err != nil {
		return model.Dynamic{}, &APIError{Kind: ErrorSchema, Message: "invalid dynamic item: " + err.Error()}
	}
	if item.ID == "" || item.Type == "" || item.Modules.Author.PubTS == 0 {
		return model.Dynamic{}, &APIError{Kind: ErrorSchema, Message: "dynamic item is missing id, type, or publication time"}
	}
	if !supportedDynamicTypes[item.Type] {
		return model.Dynamic{}, &APIError{Kind: ErrorSchema, Message: "unsupported dynamic type " + item.Type}
	}
	dynamic := model.Dynamic{
		ID:          item.ID,
		UID:         uid,
		UPName:      item.Modules.Author.Name,
		Type:        item.Type,
		PublishedAt: time.Unix(int64(item.Modules.Author.PubTS), 0).UTC(),
		URL:         "https://t.bilibili.com/" + item.ID,
	}
	if mid := rawString(item.Modules.Author.MID); mid != "" {
		dynamic.UID = mid
	}
	if item.Modules.Dynamic.Desc != nil {
		dynamic.Summary = strings.TrimSpace(item.Modules.Dynamic.Desc.Text)
		appendLinks(&dynamic, item.Modules.Dynamic.Desc.RichTextNodes)
	}
	if item.Modules.Stat != nil {
		dynamic.Stats = &model.DynamicStats{
			Forwards: item.Modules.Stat.Forward.Count,
			Comments: item.Modules.Stat.Comment.Count,
			Likes:    item.Modules.Stat.Like.Count,
		}
	}
	if item.Type != "DYNAMIC_TYPE_LIVE_RCMD" {
		if err := parseMajor(&dynamic, item.Modules.Dynamic.Major); err != nil {
			return model.Dynamic{}, err
		}
	}
	if item.Type == "DYNAMIC_TYPE_FORWARD" && len(item.Orig) > 0 && string(item.Orig) != "null" {
		original, err := parseDynamicItem("", item.Orig)
		if err != nil {
			return model.Dynamic{}, fmt.Errorf("parsing forwarded dynamic: %w", err)
		}
		dynamic.Original = &original
	}
	return dynamic, nil
}

func parseMajor(dynamic *model.Dynamic, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		if dynamic.Type == "DYNAMIC_TYPE_WORD" || dynamic.Type == "DYNAMIC_TYPE_FORWARD" {
			return nil
		}
		return &APIError{Kind: ErrorSchema, Message: "dynamic type " + dynamic.Type + " is missing its content card"}
	}
	var major rawMajor
	if err := json.Unmarshal(raw, &major); err != nil {
		return &APIError{Kind: ErrorSchema, Message: "invalid dynamic major: " + err.Error()}
	}
	recognized := 0
	if major.Archive != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_AV" {
			return unexpectedMajor(dynamic.Type, "archive")
		}
		dynamic.Title = strings.TrimSpace(major.Archive.Title)
		dynamic.Description = strings.TrimSpace(major.Archive.Desc)
		dynamic.TargetURL = webURL(major.Archive.JumpURL)
		dynamic.Badge = strings.TrimSpace(major.Archive.Badge.Text)
		appendMedia(dynamic, model.DynamicMediaCover, major.Archive.Cover, 0, 0)
		dynamic.Video = &model.DynamicVideo{
			Duration: strings.TrimSpace(major.Archive.DurationText),
			Views:    strings.TrimSpace(string(major.Archive.Stat.Play)),
			Danmaku:  strings.TrimSpace(string(major.Archive.Stat.Danmaku)),
		}
	}
	if major.Draw != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_DRAW" {
			return unexpectedMajor(dynamic.Type, "draw")
		}
		for _, picture := range major.Draw.Items {
			appendMedia(dynamic, model.DynamicMediaImage, picture.Src, int(picture.Width), int(picture.Height))
		}
	}
	if major.Article != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_ARTICLE" {
			return unexpectedMajor(dynamic.Type, "article")
		}
		dynamic.Title = strings.TrimSpace(major.Article.Title)
		dynamic.Description = strings.TrimSpace(major.Article.Desc)
		dynamic.TargetURL = webURL(major.Article.JumpURL)
		dynamic.Badge = strings.TrimSpace(major.Article.Label)
		for _, cover := range major.Article.Covers {
			appendMedia(dynamic, model.DynamicMediaCover, cover, 0, 0)
		}
	}
	if major.PGC != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_PGC" {
			return unexpectedMajor(dynamic.Type, "pgc")
		}
		dynamic.Title = strings.TrimSpace(major.PGC.Title)
		dynamic.TargetURL = webURL(major.PGC.JumpURL)
		dynamic.Badge = strings.TrimSpace(major.PGC.Badge.Text)
		appendMedia(dynamic, model.DynamicMediaCover, major.PGC.Cover, 0, 0)
	}
	if major.Common != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_COMMON_SQUARE" {
			return unexpectedMajor(dynamic.Type, "common")
		}
		dynamic.Title = strings.TrimSpace(major.Common.Title)
		dynamic.Description = strings.TrimSpace(major.Common.Desc)
		dynamic.TargetURL = webURL(major.Common.JumpURL)
		dynamic.Badge = strings.TrimSpace(major.Common.Badge.Text)
		appendMedia(dynamic, model.DynamicMediaCover, major.Common.Cover, 0, 0)
	}
	if major.Opus != nil {
		recognized++
		if dynamic.Type != "DYNAMIC_TYPE_DRAW" && dynamic.Type != "DYNAMIC_TYPE_ARTICLE" {
			return unexpectedMajor(dynamic.Type, "opus")
		}
		dynamic.Title = strings.TrimSpace(major.Opus.Title)
		dynamic.Description = strings.TrimSpace(major.Opus.Summary.Text)
		dynamic.TargetURL = webURL(major.Opus.JumpURL)
		appendLinks(dynamic, major.Opus.Summary.RichTextNodes)
		for _, picture := range major.Opus.Pics {
			pictureURL := picture.URL
			if pictureURL == "" {
				pictureURL = picture.Src
			}
			appendMedia(dynamic, model.DynamicMediaImage, pictureURL, int(picture.Width), int(picture.Height))
		}
	}
	if recognized == 0 {
		return &APIError{Kind: ErrorSchema, Message: "dynamic major has no supported content card"}
	}
	if recognized > 1 {
		return &APIError{Kind: ErrorSchema, Message: "dynamic major contains multiple content cards"}
	}
	return nil
}

func unexpectedMajor(dynamicType, majorType string) error {
	return &APIError{Kind: ErrorSchema, Message: fmt.Sprintf("dynamic type %s contains %s major", dynamicType, majorType)}
}

func appendMedia(dynamic *model.Dynamic, kind model.DynamicMediaKind, rawURL string, width, height int) {
	if mediaURL := webURL(rawURL); mediaURL != "" {
		dynamic.Media = append(dynamic.Media, model.DynamicMedia{Kind: kind, URL: mediaURL, Width: width, Height: height})
	}
}

func appendLinks(dynamic *model.Dynamic, nodes []rawRichTextNode) {
	for _, node := range nodes {
		if link := webURL(node.JumpURL); link != "" {
			text := strings.TrimSpace(node.OrigText)
			if text == "" {
				text = strings.TrimSpace(node.Text)
			}
			dynamic.Links = append(dynamic.Links, model.DynamicLink{Text: text, URL: link})
		}
	}
}

func webURL(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func ParseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return time.Duration(seconds) * time.Second
}

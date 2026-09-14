package bilibili

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"golang.org/x/net/html"
)

const refreshPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0Eg
Uc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71
nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40
JNrRuoEUXpabUzGB8QIDAQAB
-----END PUBLIC KEY-----`

// ErrRefreshRejected means renewal credentials were rejected; the current
// login may still be valid and must be checked independently.
var ErrRefreshRejected = errors.New("Bilibili renewal credentials rejected")

// WithWebURL sets the website endpoint used for renewal CSRF exchange.
func WithWebURL(endpoint string) Option {
	return func(c *Client) { c.webURL = strings.TrimRight(endpoint, "/") }
}

// SessionClient implements the renewal protocol using explicit snapshots. It
// shares transport policy with Client, but never its mutable cookies or a jar.
type SessionClient struct {
	client *Client
	http   *http.Client
}

func NewSessionClient(client *Client) *SessionClient {
	transport := *client.httpClient
	transport.Jar = nil
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &SessionClient{client: client, http: &transport}
}

type RefreshInfo struct {
	Required  bool
	Timestamp int64
}

func (c *SessionClient) Check(ctx context.Context, session model.BiliSession) (RefreshInfo, error) {
	var info RefreshInfo
	err := c.request(ctx, "/x/passport-login/web/cookie/info", http.MethodGet, c.client.passportURL+"/x/passport-login/web/cookie/info", session, nil, func(_ *http.Response, body []byte) error {
		var data struct {
			Refresh   *bool `json:"refresh"`
			Timestamp int64 `json:"timestamp"`
		}
		if err := decodeSessionResponse(body, &data); err != nil {
			return err
		}
		if data.Refresh == nil || (*data.Refresh && data.Timestamp <= 0) {
			return sessionSchema("missing refresh flag or timestamp")
		}
		info = RefreshInfo{Required: *data.Refresh, Timestamp: data.Timestamp}
		return nil
	})
	return info, err
}

func (c *SessionClient) Refresh(ctx context.Context, session model.BiliSession, timestamp int64) (model.BiliSession, error) {
	if session.RefreshToken == "" || session.Cookies["bili_jct"] == "" {
		return model.BiliSession{}, ErrRefreshRejected
	}
	if timestamp <= 0 {
		return model.BiliSession{}, sessionSchema("invalid refresh timestamp")
	}
	block, _ := pem.Decode([]byte(refreshPublicKey))
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return model.BiliSession{}, sessionSchema("invalid refresh public key")
	}
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key.(*rsa.PublicKey), fmt.Appendf(nil, "refresh_%d", timestamp), nil)
	if err != nil {
		return model.BiliSession{}, sessionSchema("unable to generate refresh path")
	}
	var csrf string
	err = c.request(ctx, "/correspond/1/{correspondPath}", http.MethodGet, c.client.webURL+"/correspond/1/"+hex.EncodeToString(encrypted), session, nil, func(_ *http.Response, body []byte) error {
		var err error
		csrf, err = refreshCSRF(body)
		return err
	})
	if err != nil {
		return model.BiliSession{}, err
	}
	var next model.BiliSession
	err = c.request(ctx, "/x/passport-login/web/cookie/refresh", http.MethodPost, c.client.passportURL+"/x/passport-login/web/cookie/refresh", session, url.Values{
		"csrf": {session.Cookies["bili_jct"]}, "refresh_csrf": {csrf}, "source": {"main_web"}, "refresh_token": {session.RefreshToken},
	}, func(response *http.Response, body []byte) error {
		var data struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeSessionResponse(body, &data); err != nil {
			return err
		}
		cookies := maps.Clone(session.Cookies)
		var hasSession, hasCSRF bool
		for _, cookie := range response.Cookies() {
			if cookie.MaxAge < 0 || cookie.Value == "" {
				delete(cookies, cookie.Name)
				continue
			}
			cookies[cookie.Name] = cookie.Value
			hasSession = hasSession || cookie.Name == "SESSDATA"
			hasCSRF = hasCSRF || cookie.Name == "bili_jct"
		}
		if !hasSession || !hasCSRF || cookies["SESSDATA"] == "" || cookies["bili_jct"] == "" || data.RefreshToken == "" {
			return sessionSchema("incomplete refreshed credentials")
		}
		next = session
		next.Cookies, next.RefreshToken = cookies, data.RefreshToken
		return nil
	})
	return next, err
}

func (c *SessionClient) Confirm(ctx context.Context, session model.BiliSession) error {
	return c.request(ctx, "/x/passport-login/web/confirm/refresh", http.MethodPost, c.client.passportURL+"/x/passport-login/web/confirm/refresh", session, url.Values{
		"csrf": {session.Cookies["bili_jct"]}, "refresh_token": {session.PendingRefreshToken},
	}, func(_ *http.Response, body []byte) error { return decodeSessionResponse(body, nil) })
}

func (c *SessionClient) Validate(ctx context.Context, session model.BiliSession) (model.BiliAccount, error) {
	var account model.BiliAccount
	err := c.request(ctx, "/x/web-interface/nav", http.MethodGet, c.client.apiURL+"/x/web-interface/nav", session, nil, func(_ *http.Response, body []byte) error {
		var data struct {
			IsLogin *bool           `json:"isLogin"`
			MID     json.RawMessage `json:"mid"`
			Name    string          `json:"uname"`
		}
		if err := decodeSessionResponse(body, &data); err != nil {
			return err
		}
		if data.IsLogin == nil {
			return sessionSchema("missing login status")
		}
		if !*data.IsLogin {
			return &APIError{Kind: ErrorAuthentication, Message: "session is not logged in"}
		}
		uid := rawString(data.MID)
		if uid == "" || uid == "0" {
			return sessionSchema("missing account identity")
		}
		account = model.BiliAccount{UID: uid, Name: strings.TrimSpace(data.Name)}
		return nil
	})
	return account, err
}

func refreshCSRF(body []byte) (string, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(string(body)))
	found := false
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return "", sessionSchema("missing refresh CSRF")
		case html.StartTagToken:
			token := tokenizer.Token()
			found = false
			if token.Data == "div" {
				for _, attr := range token.Attr {
					if attr.Key == "id" && attr.Val == "1-name" {
						found = true
					}
				}
			}
		case html.TextToken:
			if found {
				value := strings.TrimSpace(string(tokenizer.Text()))
				if value != "" {
					return value, nil
				}
			}
		case html.EndTagToken:
			found = false
		}
	}
}

func sessionSchema(message string) error { return &APIError{Kind: ErrorSchema, Message: message} }

func decodeSessionResponse(body []byte, dst any) error {
	var env struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Code == nil {
		return sessionSchema("invalid renewal envelope")
	}
	if *env.Code != 0 {
		switch *env.Code {
		case -111, 86095:
			return ErrRefreshRejected
		case -101:
			return &APIError{Kind: ErrorAuthentication, Code: *env.Code, Message: "session is not logged in"}
		case -352, -412:
			return &APIError{Kind: ErrorRiskControl, Code: *env.Code, Message: "renewal request paused"}
		default:
			return &APIError{Kind: ErrorTemporary, Code: *env.Code, Message: "renewal request failed"}
		}
	}
	if dst != nil {
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return sessionSchema("missing renewal data")
		}
		if err := json.Unmarshal(env.Data, dst); err != nil {
			return sessionSchema("invalid renewal data")
		}
	}
	return nil
}

// operation is a static route template; it must never be derived from endpoint.
func (c *SessionClient) request(parent context.Context, operation, method, endpoint string, session model.BiliSession, form url.Values, decode func(*http.Response, []byte) error) (err error) {
	parent, finish := c.observeRequest(parent, operation)
	var resp *http.Response
	defer func() { finish(resp, err) }()
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return sessionSchema("invalid renewal endpoint")
	}
	c.client.addHeaders(req, false)
	req.Header.Set("Origin", "https://www.bilibili.com")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, value := range session.Cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	resp, err = c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Transport errors can contain the secret CorrespondPath or request body.
		return &APIError{Kind: ErrorTemporary, Message: "renewal transport failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		kind := ErrorTemporary
		if resp.StatusCode == 401 {
			kind = ErrorAuthentication
		}
		if resp.StatusCode == 429 || resp.StatusCode == 412 || resp.StatusCode == 403 {
			kind = ErrorRiskControl
		}
		retry := time.Duration(0)
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 0 {
			retry = time.Duration(min(seconds, int64((1<<63-1)/int64(time.Second)))) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			retry = max(0, time.Until(date))
		}
		return &APIError{Kind: kind, HTTPStatus: resp.StatusCode, RetryAfter: retry, Message: "renewal HTTP request failed"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &APIError{Kind: ErrorTemporary, Message: "reading renewal response failed"}
	}
	if len(body) > 1<<20 {
		return sessionSchema("renewal response too large")
	}
	return decode(resp, body)
}

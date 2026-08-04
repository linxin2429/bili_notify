package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	mail "github.com/wneessen/go-mail"
)

type Sender interface {
	Send(context.Context, Message) error
}

type SettingsUpdater func(map[string]string) error

type Message struct {
	Subject string
	Text    string
	HTML    string
}

type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func IsPermanent(err error) bool {
	var permanent *PermanentError
	return errors.As(err, &permanent)
}

func NewSender(ch model.Channel, client *http.Client, updateSettings SettingsUpdater) (Sender, error) {
	if err := ch.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	switch ch.Type {
	case model.ChannelEmail:
		return newEmailSender(ch.Settings)
	case model.ChannelMicrosoft:
		return newMicrosoftSender(ch.Settings, client, updateSettings, microsoftEndpointsFor(ch.Settings)), nil
	case model.ChannelDingTalk:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], secret: ch.Settings["secret"], client: client}, nil
	case model.ChannelFeishu:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], secret: ch.Settings["secret"], client: client}, nil
	case model.ChannelWeCom:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

func DynamicMessage(d model.Dynamic) Message {
	if d.Type == "SYSTEM" {
		return Message{Subject: "[Bili Notify] 系统状态变更", Text: d.Summary, HTML: "<p>" + strings.ReplaceAll(html.EscapeString(d.Summary), "\n", "<br>") + "</p>"}
	}
	typeName := map[string]string{
		"DYNAMIC_TYPE_WORD":          "文字动态",
		"DYNAMIC_TYPE_DRAW":          "图片动态",
		"DYNAMIC_TYPE_AV":            "视频投稿",
		"DYNAMIC_TYPE_ARTICLE":       "专栏",
		"DYNAMIC_TYPE_FORWARD":       "转发动态",
		"DYNAMIC_TYPE_PGC":           "番剧内容",
		"DYNAMIC_TYPE_COMMON_SQUARE": "动态",
	}[d.Type]
	if typeName == "" {
		typeName = d.Type
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	published := d.PublishedAt.In(location).Format("2006-01-02 15:04:05 MST")
	subject := fmt.Sprintf("[B站动态] %s 发布了%s", d.UPName, typeName)
	text := fmt.Sprintf("UP主：%s\n类型：%s\n发布时间：%s\n\n%s\n\n原文：%s", d.UPName, typeName, published, d.Summary, d.URL)
	htmlBody := fmt.Sprintf(
		"<h2>%s</h2><p><strong>UP主：</strong>%s<br><strong>类型：</strong>%s<br><strong>发布时间：</strong>%s</p><p>%s</p><p><a href=\"%s\">查看原动态</a></p>",
		html.EscapeString(subject), html.EscapeString(d.UPName), html.EscapeString(typeName), html.EscapeString(published),
		strings.ReplaceAll(html.EscapeString(d.Summary), "\n", "<br>"), html.EscapeString(d.URL),
	)
	return Message{Subject: subject, Text: text, HTML: htmlBody}
}

type emailSender struct {
	client *mail.Client
	from   string
	to     []string
}

func newEmailSender(settings map[string]string) (*emailSender, error) {
	port, err := strconv.Atoi(settings["port"])
	if err != nil {
		return nil, fmt.Errorf("parsing SMTP port: %w", err)
	}
	opts := []mail.Option{
		mail.WithPort(port),
		mail.WithTimeout(10 * time.Second),
		mail.WithTLSPolicy(mail.TLSMandatory),
		mail.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings["host"]}),
	}
	if settings["tls"] == "tls" {
		opts = append(opts, mail.WithSSL())
	}
	if settings["username"] != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain), mail.WithUsername(settings["username"]), mail.WithPassword(settings["password"]))
	}
	client, err := mail.NewClient(settings["host"], opts...)
	if err != nil {
		return nil, fmt.Errorf("creating SMTP client: %w", err)
	}
	to := make([]string, 0)
	for recipient := range strings.SplitSeq(settings["to"], ",") {
		to = append(to, strings.TrimSpace(recipient))
	}
	return &emailSender{client: client, from: settings["from"], to: to}, nil
}

func (s *emailSender) Send(ctx context.Context, message Message) error {
	msg := mail.NewMsg()
	if err := msg.From(s.from); err != nil {
		return &PermanentError{Err: fmt.Errorf("setting sender: %w", err)}
	}
	if err := msg.To(s.to...); err != nil {
		return &PermanentError{Err: fmt.Errorf("setting recipients: %w", err)}
	}
	msg.Subject(message.Subject)
	msg.SetBodyString(mail.TypeTextPlain, message.Text)
	msg.AddAlternativeString(mail.TypeTextHTML, message.HTML)
	if err := s.client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	return nil
}

type robotSender struct {
	kind    model.ChannelType
	webhook string
	secret  string
	client  *http.Client
}

func (s *robotSender) Send(ctx context.Context, message Message) error {
	endpoint := s.webhook
	var payload any
	switch s.kind {
	case model.ChannelDingTalk:
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sign := hmacBase64([]byte(s.secret), []byte(timestamp+"\n"+s.secret))
		u, err := url.Parse(endpoint)
		if err != nil {
			return &PermanentError{Err: err}
		}
		q := u.Query()
		q.Set("timestamp", timestamp)
		q.Set("sign", sign)
		u.RawQuery = q.Encode()
		endpoint = u.String()
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": message.Subject, "text": message.Text}}
	case model.ChannelFeishu:
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		key := []byte(timestamp + "\n" + s.secret)
		sign := hmacBase64(key, nil)
		payload = map[string]any{"timestamp": timestamp, "sign": sign, "msg_type": "text", "content": map[string]string{"text": message.Text}}
	case model.ChannelWeCom:
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": message.Text}}
	default:
		return &PermanentError{Err: fmt.Errorf("unsupported robot type %q", s.kind)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting %s notification: %w", s.kind, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading %s response: %w", s.kind, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("%s returned HTTP %d", s.kind, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &PermanentError{Err: fmt.Errorf("%s returned HTTP %d", s.kind, resp.StatusCode)}
	}
	var result map[string]any
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return &PermanentError{Err: fmt.Errorf("decoding %s response: %w", s.kind, err)}
	}
	if code := businessCode(result); code != 0 {
		return &PermanentError{Err: fmt.Errorf("%s returned business code %d: %s", s.kind, code, responseBody)}
	}
	return nil
}

func businessCode(result map[string]any) int64 {
	for _, key := range []string{"errcode", "code", "StatusCode"} {
		value, ok := result[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return int64(v)
		case string:
			code, _ := strconv.ParseInt(v, 10, 64)
			return code
		}
	}
	return 0
}

func hmacBase64(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

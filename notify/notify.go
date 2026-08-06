package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	mail "github.com/wneessen/go-mail"
)

type Sender interface {
	Send(context.Context, Message) error
}

// ProgressiveSender optionally supports multi-part delivery with resumable progress.
type ProgressiveSender interface {
	Sender
	SendProgressive(context.Context, Message, *model.DeliveryProgress) (*model.DeliveryProgress, error)
}

type SettingsUpdater func(map[string]string) error

type Message struct {
	Subject  string
	Sections []Section
	Action   Link
}

type Section struct {
	Heading    string
	Paragraphs []string
	Facts      []Fact
	Links      []Link
	Images     []Image
}

type Fact struct {
	Label string
	Value string
}

type Link struct {
	Label string
	URL   string
}

type Image struct {
	Label       string
	URL         string
	LocalPath   string
	ContentType string
}

type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func IsPermanent(err error) bool {
	var permanent *PermanentError
	return errors.As(err, &permanent)
}

func NewSender(ch model.Channel, client *http.Client, dataDir string, updateSettings SettingsUpdater) (Sender, error) {
	if err := ch.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	switch ch.Type {
	case model.ChannelEmail:
		return newEmailSender(ch.Settings, dataDir)
	case model.ChannelMicrosoft:
		return newMicrosoftSender(ch.Settings, client, dataDir, updateSettings, microsoftEndpointsFor(ch.Settings)), nil
	case model.ChannelDingTalk:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], secret: ch.Settings["secret"], client: client, dataDir: dataDir}, nil
	case model.ChannelFeishu:
		return &robotSender{
			kind: ch.Type, webhook: ch.Settings["webhook"], secret: ch.Settings["secret"], client: client, dataDir: dataDir,
			appID: ch.Settings["app_id"], appSecret: ch.Settings["app_secret"],
		}, nil
	case model.ChannelWeCom:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], client: client, dataDir: dataDir}, nil
	default:
		return nil, fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

func DynamicMessage(d model.Dynamic) Message {
	if d.Type == "SYSTEM" {
		return TextMessage("[Bili Notify] 系统状态变更", d.Summary)
	}
	typeName := dynamicTypeName(d.Type)
	subject := fmt.Sprintf("[B站动态] %s 发布了%s", d.UPName, typeName)
	message := Message{
		Subject:  subject,
		Sections: []Section{dynamicSection(d, false)},
		Action:   Link{Label: "查看原动态", URL: d.URL},
	}
	for original := d.Original; original != nil; original = original.Original {
		message.Sections = append(message.Sections, dynamicSection(*original, true))
	}
	if d.Type == "DYNAMIC_TYPE_FORWARD" && d.Original == nil {
		message.Sections = append(message.Sections, Section{Heading: "转发原动态", Paragraphs: []string{"原动态已删除或不可用。"}})
	}
	return message
}

func CommentThreadMessage(n model.CommentNotification) Message {
	subject := fmt.Sprintf("[B站评论] %s 回复了评论", n.UPName)
	contentLabel := dynamicTypeName(n.ContentType)
	if contentLabel == n.ContentType {
		contentLabel = "内容"
	}
	section := Section{
		Heading: firstNonEmpty(n.ContentTitle, contentLabel),
		Facts: []Fact{
			{Label: "UP主", Value: n.UPName},
			{Label: "内容类型", Value: contentLabel},
			{Label: "回复时间", Value: n.PublishedAt.In(time.Local).Format("2006-01-02 15:04:05 MST")},
		},
	}
	if n.ContentURL != "" {
		section.Links = append(section.Links, Link{Label: "查看内容", URL: n.ContentURL})
	}
	if n.Incomplete {
		section.Paragraphs = append(section.Paragraphs, "对话串可能不完整：子评论翻页达到上限。")
	}
	section.Paragraphs = append(section.Paragraphs, "对话：")
	for i, node := range n.Thread {
		prefix := ""
		if i > 0 {
			prefix = strings.Repeat("↳ ", min(i, 3))
		}
		label := node.Name
		if label == "" {
			label = node.Mid
		}
		if node.IsUP {
			label += "（UP）"
		}
		if node.IsTrigger {
			label += " ★"
		}
		line := prefix + label + "：" + node.Message
		section.Paragraphs = append(section.Paragraphs, line)
	}
	actionURL := n.ContentURL
	return Message{
		Subject:  subject,
		Sections: []Section{section},
		Action:   Link{Label: "查看内容", URL: actionURL},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func TextMessage(subject, body string) Message {
	return Message{Subject: subject, Sections: []Section{{Paragraphs: []string{body}}}}
}

func dynamicTypeName(dynamicType string) string {
	typeName := map[string]string{
		"DYNAMIC_TYPE_WORD":          "文字动态",
		"DYNAMIC_TYPE_DRAW":          "图片动态",
		"DYNAMIC_TYPE_AV":            "视频投稿",
		"DYNAMIC_TYPE_ARTICLE":       "专栏",
		"DYNAMIC_TYPE_FORWARD":       "转发动态",
		"DYNAMIC_TYPE_PGC":           "番剧内容",
		"DYNAMIC_TYPE_COMMON_SQUARE": "动态",
	}[dynamicType]
	if typeName == "" {
		return dynamicType
	}
	return typeName
}

func dynamicSection(d model.Dynamic, forwarded bool) Section {
	published := d.PublishedAt.In(time.Local).Format("2006-01-02 15:04:05 MST")
	heading := d.Title
	if forwarded {
		heading = "转发自 " + d.UPName
		if d.Title != "" {
			heading += " · " + d.Title
		}
	}
	section := Section{
		Heading: heading,
		Facts: []Fact{
			{Label: "UP主", Value: d.UPName},
			{Label: "类型", Value: dynamicTypeName(d.Type)},
			{Label: "发布时间", Value: published},
		},
	}
	if d.Badge != "" {
		section.Facts = append(section.Facts, Fact{Label: "标记", Value: d.Badge})
	}
	if d.Summary != "" {
		section.Paragraphs = append(section.Paragraphs, d.Summary)
	}
	if d.Description != "" {
		section.Paragraphs = append(section.Paragraphs, d.Description)
	}
	if d.Video != nil {
		if d.Video.Duration != "" {
			section.Facts = append(section.Facts, Fact{Label: "时长", Value: d.Video.Duration})
		}
		if d.Video.Views != "" {
			section.Facts = append(section.Facts, Fact{Label: "播放", Value: d.Video.Views})
		}
		if d.Video.Danmaku != "" {
			section.Facts = append(section.Facts, Fact{Label: "弹幕", Value: d.Video.Danmaku})
		}
	}
	if d.Stats != nil {
		section.Facts = append(section.Facts,
			Fact{Label: "转发", Value: strconv.FormatInt(d.Stats.Forwards, 10)},
			Fact{Label: "评论", Value: strconv.FormatInt(d.Stats.Comments, 10)},
			Fact{Label: "点赞", Value: strconv.FormatInt(d.Stats.Likes, 10)},
		)
	}
	if d.TargetURL != "" {
		section.Links = append(section.Links, Link{Label: "查看内容", URL: d.TargetURL})
	}
	if forwarded && d.URL != "" {
		section.Links = append(section.Links, Link{Label: "查看被转发动态", URL: d.URL})
	}
	for _, link := range d.Links {
		label := link.Text
		if label == "" {
			label = "正文链接"
		}
		section.Links = append(section.Links, Link{Label: label, URL: link.URL})
	}
	for i, media := range d.Media {
		label := fmt.Sprintf("图片 %d", i+1)
		if media.Kind == model.DynamicMediaCover {
			label = "封面"
		}
		section.Images = append(section.Images, Image{
			Label: label, URL: media.URL, LocalPath: media.LocalPath, ContentType: media.ContentType,
		})
	}
	return section
}

type emailSender struct {
	client  *mail.Client
	from    string
	to      []string
	dataDir string
}

func newEmailSender(settings map[string]string, dataDir string) (*emailSender, error) {
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
	return &emailSender{client: client, from: settings["from"], to: to, dataDir: dataDir}, nil
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
	cidByPath := map[string]string{}
	htmlBody := renderHTMLWithCID(message, func(image Image, index int) string {
		if image.LocalPath == "" || s.dataDir == "" {
			return ""
		}
		if cid, ok := cidByPath[image.LocalPath]; ok {
			return cid
		}
		data, contentType, err := media.ReadFile(s.dataDir, image.LocalPath)
		if err != nil {
			return ""
		}
		cid := fmt.Sprintf("image-%d", index)
		name := filepath.Base(image.LocalPath)
		if name == "." || name == "/" || name == "" {
			name = cid
		}
		opts := []mail.FileOption{mail.WithFileContentID(cid), mail.WithFileName(name)}
		if contentType != "" {
			opts = append(opts, mail.WithFileContentType(mail.ContentType(contentType)))
		}
		if err := msg.EmbedReader(name, bytes.NewReader(data), opts...); err != nil {
			return ""
		}
		cidByPath[image.LocalPath] = cid
		return cid
	})
	msg.SetBodyString(mail.TypeTextPlain, renderPlainText(message))
	msg.AddAlternativeString(mail.TypeTextHTML, htmlBody)
	if err := s.client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	return nil
}

type robotSender struct {
	kind      model.ChannelType
	webhook   string
	secret    string
	client    *http.Client
	dataDir   string
	appID     string
	appSecret string
}

func (s *robotSender) Send(ctx context.Context, message Message) error {
	if s.kind == model.ChannelWeCom {
		_, err := s.SendProgressive(ctx, message, nil)
		return err
	}
	endpoint, payload, err := s.buildPayload(ctx, message)
	if err != nil {
		return err
	}
	return s.postJSON(ctx, endpoint, payload)
}

func (s *robotSender) SendProgressive(ctx context.Context, message Message, progress *model.DeliveryProgress) (*model.DeliveryProgress, error) {
	if s.kind != model.ChannelWeCom {
		if err := s.Send(ctx, message); err != nil {
			return progress, err
		}
		return progress, nil
	}
	current := model.DeliveryProgress{}
	if progress != nil {
		current = *progress
	}
	images := collectLocalImages(message, s.dataDir, media.WeComMaxImageSize)
	if !current.TextSent {
		payload := map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": renderMarkdown(message, 4096, true, false)}}
		if err := s.postJSON(ctx, s.webhook, payload); err != nil {
			return &current, err
		}
		current.TextSent = true
	}
	for i := current.ImagesSent; i < len(images); i++ {
		img := images[i]
		sum := md5.Sum(img.data)
		payload := map[string]any{
			"msgtype": "image",
			"image": map[string]string{
				"base64": base64.StdEncoding.EncodeToString(img.data),
				"md5":    hex.EncodeToString(sum[:]),
			},
		}
		if err := s.postJSON(ctx, s.webhook, payload); err != nil {
			return &current, err
		}
		current.ImagesSent = i + 1
	}
	return &current, nil
}

type localImage struct {
	data        []byte
	contentType string
	name        string
}

func collectLocalImages(message Message, dataDir string, maxSize int64) []localImage {
	if dataDir == "" {
		return nil
	}
	out := make([]localImage, 0)
	for _, section := range message.Sections {
		for _, image := range section.Images {
			if image.LocalPath == "" {
				continue
			}
			data, contentType, err := media.ReadFile(dataDir, image.LocalPath)
			if err != nil || len(data) == 0 {
				continue
			}
			if maxSize > 0 && int64(len(data)) > maxSize {
				continue
			}
			if image.ContentType != "" {
				contentType = image.ContentType
			}
			out = append(out, localImage{data: data, contentType: contentType, name: filepath.Base(image.LocalPath)})
		}
	}
	return out
}

func (s *robotSender) buildPayload(ctx context.Context, message Message) (endpoint string, payload any, err error) {
	endpoint = s.webhook
	switch s.kind {
	case model.ChannelDingTalk:
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sign := hmacBase64([]byte(s.secret), []byte(timestamp+"\n"+s.secret))
		u, parseErr := url.Parse(endpoint)
		if parseErr != nil {
			return "", nil, &PermanentError{Err: parseErr}
		}
		q := u.Query()
		q.Set("timestamp", timestamp)
		q.Set("sign", sign)
		u.RawQuery = q.Encode()
		endpoint = u.String()
		payload = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": message.Subject, "text": renderMarkdown(message, 20_000, false, true)}}
	case model.ChannelFeishu:
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		key := []byte(timestamp + "\n" + s.secret)
		sign := hmacBase64(key, nil)
		keys, uploadErr := s.uploadFeishuImages(ctx, message)
		if uploadErr != nil {
			return "", nil, uploadErr
		}
		payload = renderFeishuPayload(message, timestamp, sign, keys)
	default:
		return "", nil, &PermanentError{Err: fmt.Errorf("unsupported robot type %q", s.kind)}
	}
	return endpoint, payload, nil
}

func (s *robotSender) postJSON(ctx context.Context, endpoint string, payload any) error {
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
		return &PermanentError{Err: fmt.Errorf("%s returned business code %d", s.kind, code)}
	}
	return nil
}

type feishuTokenCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

var feishuTokens sync.Map // appID -> *feishuTokenCache

func (s *robotSender) uploadFeishuImages(ctx context.Context, message Message) (map[string]string, error) {
	if strings.TrimSpace(s.appID) == "" || strings.TrimSpace(s.appSecret) == "" || s.dataDir == "" {
		return nil, nil
	}
	token, err := s.feishuTenantToken(ctx)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]string)
	for _, section := range message.Sections {
		for _, image := range section.Images {
			if image.LocalPath == "" {
				continue
			}
			if _, ok := keys[image.LocalPath]; ok {
				continue
			}
			data, contentType, err := media.ReadFile(s.dataDir, image.LocalPath)
			if err != nil || len(data) == 0 {
				continue
			}
			if image.ContentType != "" {
				contentType = image.ContentType
			}
			key, err := s.uploadFeishuImage(ctx, token, localImage{
				data: data, contentType: contentType, name: filepath.Base(image.LocalPath),
			})
			if err != nil {
				return nil, err
			}
			keys[image.LocalPath] = key
		}
	}
	return keys, nil
}

func (s *robotSender) feishuTenantToken(ctx context.Context) (string, error) {
	raw, _ := feishuTokens.LoadOrStore(s.appID, &feishuTokenCache{})
	cache := raw.(*feishuTokenCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.token != "" && time.Now().Before(cache.expires) {
		return cache.token, nil
	}
	body, err := json.Marshal(map[string]string{"app_id": s.appID, "app_secret": s.appSecret})
	if err != nil {
		return "", &PermanentError{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", &PermanentError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting feishu tenant token: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading feishu tenant token: %w", err)
	}
	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", &PermanentError{Err: fmt.Errorf("decoding feishu tenant token: %w", err)}
	}
	if result.Code != 0 || result.TenantAccessToken == "" {
		return "", &PermanentError{Err: fmt.Errorf("feishu tenant token failed: code=%d msg=%s", result.Code, result.Msg)}
	}
	expire := result.Expire
	if expire <= 0 {
		expire = 7200
	}
	cache.token = result.TenantAccessToken
	cache.expires = time.Now().Add(time.Duration(expire-60) * time.Second)
	return cache.token, nil
}

func (s *robotSender) uploadFeishuImage(ctx context.Context, token string, img localImage) (string, error) {
	var body bytes.Buffer
	boundary := "bili-notify-feishu"
	_, _ = fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"image_type\"\r\n\r\nmessage\r\n", boundary)
	name := img.name
	if name == "" {
		name = "image.bin"
	}
	_, _ = fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=\"image\"; filename=%q\r\nContent-Type: %s\r\n\r\n", boundary, name, firstNonEmpty(img.contentType, "application/octet-stream"))
	_, _ = body.Write(img.data)
	_, _ = fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/images", &body)
	if err != nil {
		return "", &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading feishu image: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading feishu image upload: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", fmt.Errorf("feishu image upload returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &PermanentError{Err: fmt.Errorf("feishu image upload returned HTTP %d", resp.StatusCode)}
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImageKey string `json:"image_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", &PermanentError{Err: fmt.Errorf("decoding feishu image upload: %w", err)}
	}
	if result.Code != 0 || result.Data.ImageKey == "" {
		return "", &PermanentError{Err: fmt.Errorf("feishu image upload failed: code=%d msg=%s", result.Code, result.Msg)}
	}
	return result.Data.ImageKey, nil
}

type renderPart struct {
	text        string
	truncatable bool
}

func renderPlainText(message Message) string {
	var b strings.Builder
	b.WriteString(message.Subject)
	for _, section := range message.Sections {
		b.WriteString("\n\n")
		if section.Heading != "" {
			b.WriteString(section.Heading)
			b.WriteByte('\n')
		}
		for _, fact := range section.Facts {
			fmt.Fprintf(&b, "%s：%s\n", fact.Label, fact.Value)
		}
		for _, paragraph := range section.Paragraphs {
			b.WriteByte('\n')
			b.WriteString(paragraph)
			b.WriteByte('\n')
		}
		for _, link := range section.Links {
			fmt.Fprintf(&b, "%s：%s\n", link.Label, link.URL)
		}
		for _, image := range section.Images {
			fmt.Fprintf(&b, "%s：%s\n", image.Label, image.URL)
		}
	}
	if message.Action.URL != "" {
		fmt.Fprintf(&b, "\n%s：%s", message.Action.Label, message.Action.URL)
	}
	return strings.TrimSpace(b.String())
}

func renderHTML(message Message) string {
	return renderHTMLWithCID(message, nil)
}

// cidFor returns a content-id for an image, or empty to keep the remote URL.
func renderHTMLWithCID(message Message, cidFor func(image Image, index int) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h2>%s</h2>", html.EscapeString(message.Subject))
	imageIndex := 0
	for _, section := range message.Sections {
		b.WriteString("<section>")
		if section.Heading != "" {
			fmt.Fprintf(&b, "<h3>%s</h3>", html.EscapeString(section.Heading))
		}
		if len(section.Facts) > 0 {
			b.WriteString("<dl>")
			for _, fact := range section.Facts {
				fmt.Fprintf(&b, "<dt><strong>%s</strong></dt><dd>%s</dd>", html.EscapeString(fact.Label), html.EscapeString(fact.Value))
			}
			b.WriteString("</dl>")
		}
		for _, paragraph := range section.Paragraphs {
			fmt.Fprintf(&b, "<p>%s</p>", strings.ReplaceAll(html.EscapeString(paragraph), "\n", "<br>"))
		}
		for _, link := range section.Links {
			fmt.Fprintf(&b, "<p><a href=\"%s\">%s</a></p>", html.EscapeString(link.URL), html.EscapeString(link.Label))
		}
		for _, image := range section.Images {
			src := image.URL
			if cidFor != nil {
				if cid := cidFor(image, imageIndex); cid != "" {
					src = "cid:" + cid
				}
			}
			imageIndex++
			fmt.Fprintf(&b, "<p><img src=\"%s\" alt=\"%s\" style=\"max-width:100%%;height:auto\"></p>", html.EscapeString(src), html.EscapeString(image.Label))
		}
		b.WriteString("</section>")
	}
	if message.Action.URL != "" {
		fmt.Fprintf(&b, "<p><a href=\"%s\">%s</a></p>", html.EscapeString(message.Action.URL), html.EscapeString(message.Action.Label))
	}
	return b.String()
}

func renderMarkdown(message Message, limit int, countBytes, inlineImages bool) string {
	parts := []renderPart{{text: "## " + escapeMarkdown(message.Subject)}}
	for _, section := range message.Sections {
		if section.Heading != "" {
			parts = append(parts, renderPart{text: "### " + escapeMarkdown(section.Heading)})
		}
		for _, fact := range section.Facts {
			parts = append(parts, renderPart{text: "**" + escapeMarkdown(fact.Label) + "：** " + escapeMarkdown(fact.Value)})
		}
		if len(section.Images) > 0 {
			parts = append(parts, renderPart{text: renderMarkdownImage(section.Images[0], inlineImages)})
		}
		for _, paragraph := range section.Paragraphs {
			parts = append(parts, renderPart{text: escapeMarkdown(paragraph), truncatable: true})
		}
		for _, link := range section.Links {
			parts = append(parts, renderPart{text: markdownLink(link.Label, link.URL)})
		}
		if len(section.Images) > 1 {
			for _, image := range section.Images[1:] {
				parts = append(parts, renderPart{text: renderMarkdownImage(image, inlineImages)})
			}
		}
	}
	footer := ""
	if message.Action.URL != "" {
		footer = markdownLink(message.Action.Label, message.Action.URL)
	}
	return fitMarkdown(parts, footer, limit, countBytes)
}

func fitMarkdown(parts []renderPart, footer string, limit int, countBytes bool) string {
	separator := "\n\n"
	reserved := measure(footer, countBytes)
	if footer != "" {
		reserved += measure(separator, countBytes)
	}
	var b strings.Builder
	truncated := false
	for _, part := range parts {
		prefix := ""
		if b.Len() > 0 {
			prefix = separator
		}
		remaining := limit - reserved - measure(b.String()+prefix, countBytes)
		if remaining <= 0 {
			truncated = true
			break
		}
		if measure(part.text, countBytes) <= remaining {
			b.WriteString(prefix)
			b.WriteString(part.text)
			continue
		}
		if part.truncatable {
			marker := "…（内容已截断）"
			shortened := truncateMeasured(part.text, remaining-measure(marker, countBytes), countBytes)
			if shortened != "" {
				b.WriteString(prefix)
				b.WriteString(shortened)
				b.WriteString(marker)
			}
		}
		truncated = true
		break
	}
	if truncated && !strings.Contains(b.String(), "内容已截断") {
		marker := "…（内容已截断）"
		prefix := ""
		if b.Len() > 0 {
			prefix = separator
		}
		if measure(b.String()+prefix+marker, countBytes)+reserved <= limit {
			b.WriteString(prefix)
			b.WriteString(marker)
		}
	}
	if footer != "" {
		if b.Len() > 0 {
			b.WriteString(separator)
		}
		b.WriteString(footer)
	}
	return b.String()
}

func escapeMarkdown(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]")
	return replacer.Replace(value)
}

func markdownLink(label, target string) string {
	return "[" + escapeMarkdown(label) + "](" + markdownURL(target) + ")"
}

func renderMarkdownImage(image Image, inline bool) string {
	if !inline {
		return markdownLink(image.Label, image.URL)
	}
	return "![" + escapeMarkdown(image.Label) + "](" + markdownURL(image.URL) + ")"
}

func markdownURL(target string) string {
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(target)
}

func measure(value string, countBytes bool) int {
	if countBytes {
		return len(value)
	}
	return len([]rune(value))
}

func truncateMeasured(value string, limit int, countBytes bool) string {
	if limit <= 0 {
		return ""
	}
	if !countBytes {
		runes := []rune(value)
		return string(runes[:min(limit, len(runes))])
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

type feishuElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
}

type feishuPost struct {
	Title   string            `json:"title"`
	Content [][]feishuElement `json:"content"`
}

func renderFeishuPayload(message Message, timestamp, sign string, imageKeys map[string]string) map[string]any {
	rows := make([][]feishuElement, 0)
	for _, section := range message.Sections {
		if section.Heading != "" {
			rows = append(rows, []feishuElement{{Tag: "text", Text: section.Heading}})
		}
		for _, fact := range section.Facts {
			rows = append(rows, []feishuElement{{Tag: "text", Text: fact.Label + "：" + fact.Value}})
		}
		if len(section.Images) > 0 {
			rows = append(rows, []feishuElement{feishuImageElement(section.Images[0], imageKeys)})
		}
		for _, paragraph := range section.Paragraphs {
			rows = append(rows, []feishuElement{{Tag: "text", Text: paragraph}})
		}
		for _, link := range section.Links {
			rows = append(rows, []feishuElement{{Tag: "a", Text: link.Label, Href: link.URL}})
		}
		if len(section.Images) > 1 {
			for _, image := range section.Images[1:] {
				rows = append(rows, []feishuElement{feishuImageElement(image, imageKeys)})
			}
		}
	}
	footer := []feishuElement(nil)
	if message.Action.URL != "" {
		footer = []feishuElement{{Tag: "a", Text: message.Action.Label, Href: message.Action.URL}}
	}
	accepted := fitFeishuRows(message.Subject, rows, footer, timestamp, sign)
	return feishuPayload(message.Subject, accepted, timestamp, sign)
}

func feishuImageElement(image Image, imageKeys map[string]string) feishuElement {
	if key := imageKeys[image.LocalPath]; key != "" {
		return feishuElement{Tag: "img", ImageKey: key}
	}
	return feishuElement{Tag: "a", Text: image.Label, Href: image.URL}
}

func fitFeishuRows(title string, rows [][]feishuElement, footer []feishuElement, timestamp, sign string) [][]feishuElement {
	accepted := make([][]feishuElement, 0, len(rows)+1)
	for _, row := range rows {
		candidate := append(slices.Clone(accepted), row)
		withFooter := candidate
		if footer != nil {
			withFooter = append(slices.Clone(candidate), footer)
		}
		if payloadSize(feishuPayload(title, withFooter, timestamp, sign)) <= 20*1024 {
			accepted = candidate
			continue
		}
		if len(row) == 1 && row[0].Tag == "text" {
			shortened := row[0]
			shortened.Text = truncateFeishuText(title, accepted, shortened.Text, footer, timestamp, sign)
			if shortened.Text != "" {
				accepted = append(accepted, []feishuElement{shortened})
			}
		}
		break
	}
	if footer != nil {
		accepted = append(accepted, footer)
	}
	return accepted
}

func truncateFeishuText(title string, rows [][]feishuElement, value string, footer []feishuElement, timestamp, sign string) string {
	marker := "…（内容已截断）"
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		row := []feishuElement{{Tag: "text", Text: string(runes[:mid]) + marker}}
		candidate := append(slices.Clone(rows), row)
		if footer != nil {
			candidate = append(candidate, footer)
		}
		if payloadSize(feishuPayload(title, candidate, timestamp, sign)) <= 20*1024 {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		return ""
	}
	return string(runes[:low]) + marker
}

func feishuPayload(title string, rows [][]feishuElement, timestamp, sign string) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"sign":      sign,
		"msg_type":  "post",
		"content": map[string]any{"post": map[string]feishuPost{
			"zh_cn": {Title: title, Content: rows},
		}},
	}
}

func payloadSize(payload any) int {
	raw, _ := json.Marshal(payload)
	return len(raw)
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

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
	"mime/multipart"
	"net"
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
	platformcontract "github.com/linxin2429/bili_notify/platform"
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
	Subject    string
	Sections   []Section
	Action     Link
	AllowSplit bool
	Files      []model.DeliveryFile
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

// ActionableError carries a stable, secret-free explanation that the admin UI
// may show while preserving the detailed protocol error for logs and retries.
type ActionableError struct {
	Message string
	Err     error
}

func (e *ActionableError) Error() string { return e.Err.Error() }
func (e *ActionableError) Unwrap() error { return e.Err }

func ActionableMessage(err error) (string, bool) {
	var actionable *ActionableError
	if !errors.As(err, &actionable) || actionable.Message == "" {
		return "", false
	}
	return actionable.Message, true
}

type upstreamBusinessError struct {
	operation  string
	statusCode int
	code       int64
}

func (e *upstreamBusinessError) Error() string {
	if e.statusCode != 0 {
		return fmt.Sprintf("%s returned HTTP %d with business code %d", e.operation, e.statusCode, e.code)
	}
	return fmt.Sprintf("%s returned business code %d", e.operation, e.code)
}

func businessError(err error) (*upstreamBusinessError, bool) {
	var businessErr *upstreamBusinessError
	if !errors.As(err, &businessErr) {
		return nil, false
	}
	return businessErr, true
}

// RetryAfterError carries an upstream's minimum retry delay without exposing
// its response body. Delivery scheduling still applies the configured backoff
// when it is longer.
type RetryAfterError struct {
	Err   error
	Delay time.Duration
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// RetryAfter returns an upstream-requested minimum retry delay.
func RetryAfter(err error) (time.Duration, bool) {
	var retry *RetryAfterError
	if !errors.As(err, &retry) || retry.Delay <= 0 {
		return 0, false
	}
	return retry.Delay, true
}

const maxProtocolResponseBytes = 1 << 20

func NewSender(ch model.Channel, client *http.Client, dataDir string, updateSettings SettingsUpdater) (Sender, error) {
	if err := ch.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = defaultHTTPClient()
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
			kind: ch.Type, client: client, dataDir: dataDir, chatID: ch.Settings["chat_id"],
			appID: ch.Settings["app_id"], appSecret: ch.Settings["app_secret"],
		}, nil
	case model.ChannelWeCom:
		return &robotSender{kind: ch.Type, webhook: ch.Settings["webhook"], client: client, dataDir: dataDir}, nil
	default:
		return nil, fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

func defaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 8 * time.Second
	return &http.Client{Transport: transport}
}

func ContentMessage(content model.ContentSnapshot) Message {
	typeName := dynamicTypeName(content.UpstreamType)
	meta, _ := platformcontract.BuiltinMeta(content.Platform)
	platformName, noun := meta.DisplayName, meta.ContentNoun
	subject := fmt.Sprintf("[%s%s] %s 发布了%s", platformName, noun, content.AuthorName, typeName)
	message := Message{
		Subject:  subject,
		Sections: []Section{contentSection(content, false)},
		Action:   Link{Label: "查看原内容", URL: content.URL},
		Files:    slices.Clone(content.Files),
	}
	for original := content.ForwardOf; original != nil; original = original.ForwardOf {
		message.Sections = append(message.Sections, contentSection(*original, true))
	}
	if content.UpstreamType == "DYNAMIC_TYPE_FORWARD" && content.ForwardOf == nil {
		message.Sections = append(message.Sections, Section{Heading: "转发原动态", Paragraphs: []string{"原动态已删除或不可用。"}})
	}
	return message
}

func SystemMessage(alert model.SystemAlert) Message {
	return TextMessage(firstNonEmpty(alert.Title, "[Bili Notify] 系统状态变更"), alert.Body)
}

func CommentThreadMessage(n model.CommentNotification) Message {
	meta, _ := platformcontract.BuiltinMeta(n.Platform)
	platformName := meta.DisplayName
	authorLabel := meta.TriggerLabel
	if authorLabel == "" {
		authorLabel = "作者"
	}
	subject := fmt.Sprintf("[%s评论] %s 回复了评论", platformName, n.AuthorName)
	contentLabel := dynamicTypeName(n.ContentType)
	if contentLabel == n.ContentType {
		contentLabel = "内容"
	}
	section := Section{
		Heading: firstNonEmpty(n.ContentTitle, contentLabel),
		Facts: []Fact{
			{Label: authorLabel, Value: n.AuthorName},
			{Label: "来源", Value: n.SourceName},
			{Label: "内容类型", Value: contentLabel},
			{Label: "回复时间", Value: n.PublishedAt.In(time.Local).Format("2006-01-02 15:04:05 MST")},
		},
	}
	if n.ContentURL != "" {
		section.Links = append(section.Links, Link{Label: "查看内容", URL: n.ContentURL})
	}
	if n.Incomplete {
		section.Paragraphs = append(section.Paragraphs, "评论树可能不完整：上游分页、父节点缺失或循环引用。")
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

// AINotificationMessage renders one automatic AI terminal event.
func AINotificationMessage(value model.AINotification) Message {
	stage := "视频转写"
	if value.Stage == model.AIJobSummary {
		stage = "视频总结"
	}
	result := "完成"
	body := value.Body
	if !value.Succeeded {
		result = "失败"
		body = value.ErrorMessage
		if value.ErrorCode != "" {
			body = value.ErrorCode + "：" + body
		}
	}
	message := TextMessage(fmt.Sprintf("[Bili Notify] %s%s：%s", stage, result, value.Title), body)
	message.AllowSplit = value.Succeeded
	if value.SourceURL != "" {
		message.Action = Link{Label: "查看原视频", URL: value.SourceURL}
	}
	return message
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

func contentSection(content model.ContentSnapshot, forwarded bool) Section {
	published := content.PublishedAt.In(time.Local).Format("2006-01-02 15:04:05 MST")
	heading := content.Title
	if forwarded {
		heading = "转发自 " + content.AuthorName
		if content.Title != "" {
			heading += " · " + content.Title
		}
	}
	meta, _ := platformcontract.BuiltinMeta(content.Platform)
	authorLabel := meta.AuthorLabel
	section := Section{
		Heading: heading,
		Facts: []Fact{
			{Label: authorLabel, Value: content.AuthorName},
			{Label: "来源", Value: content.SourceName},
			{Label: "类型", Value: dynamicTypeName(content.UpstreamType)},
			{Label: "发布时间", Value: published},
		},
	}
	if content.Badge != "" {
		section.Facts = append(section.Facts, Fact{Label: "标记", Value: content.Badge})
	}
	if content.Text != "" {
		section.Paragraphs = append(section.Paragraphs, content.Text)
	}
	if content.Description != "" && content.Description != content.Text {
		section.Paragraphs = append(section.Paragraphs, content.Description)
	}
	if content.Video != nil {
		if content.Video.Duration != "" {
			section.Facts = append(section.Facts, Fact{Label: "时长", Value: content.Video.Duration})
		}
		if content.Video.Views != "" {
			section.Facts = append(section.Facts, Fact{Label: "播放", Value: content.Video.Views})
		}
		if content.Video.Danmaku != "" {
			section.Facts = append(section.Facts, Fact{Label: "弹幕", Value: content.Video.Danmaku})
		}
	}
	if content.Stats != nil {
		section.Facts = append(section.Facts,
			Fact{Label: "转发", Value: strconv.FormatInt(content.Stats["forwards"], 10)},
			Fact{Label: "评论", Value: strconv.FormatInt(content.Stats["comments"], 10)},
			Fact{Label: "点赞", Value: strconv.FormatInt(content.Stats["likes"], 10)},
		)
	}
	if content.TargetURL != "" {
		section.Links = append(section.Links, Link{Label: "查看内容", URL: content.TargetURL})
	}
	if forwarded && content.URL != "" {
		section.Links = append(section.Links, Link{Label: "查看被转发动态", URL: content.URL})
	}
	for _, link := range content.Links {
		label := link.Text
		if label == "" {
			label = "正文链接"
		}
		section.Links = append(section.Links, Link{Label: label, URL: link.URL})
	}
	for i, media := range content.Media {
		label := fmt.Sprintf("图片 %d", i+1)
		if media.Kind == string(model.DynamicMediaCover) {
			label = "封面"
		}
		section.Images = append(section.Images, Image{
			Label: label, URL: media.URL, LocalPath: media.LocalPath, ContentType: media.MIME,
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
	return newEmailSenderWithTLSConfig(settings, dataDir, nil)
}

func newEmailSenderWithTLSConfig(settings map[string]string, dataDir string, tlsConfig *tls.Config) (*emailSender, error) {
	port, err := strconv.Atoi(settings["port"])
	if err != nil {
		return nil, fmt.Errorf("parsing SMTP port: %w", err)
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings["host"]}
	} else {
		tlsConfig = tlsConfig.Clone()
		tlsConfig.MinVersion = max(tlsConfig.MinVersion, uint16(tls.VersionTLS12))
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = settings["host"]
		}
	}
	opts := []mail.Option{
		mail.WithPort(port),
		mail.WithTimeout(15 * time.Minute),
		mail.WithTLSPolicy(mail.TLSMandatory),
		mail.WithTLSConfig(tlsConfig),
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
	message, files := classifyFiles(message, s.dataDir, 1, 0)
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
	opened := make([]io.Closer, 0, len(files))
	for _, attachment := range files {
		file, _, detected, err := media.OpenFile(s.dataDir, attachment.LocalPath)
		if err != nil {
			for _, closer := range opened {
				_ = closer.Close()
			}
			return fmt.Errorf("opening email attachment %q: %w", attachment.Name, err)
		}
		opened = append(opened, file)
		contentType := firstNonEmpty(attachment.MIME, detected, "application/octet-stream")
		if err := msg.AttachReader(attachment.Name, file, mail.WithFileName(attachment.Name), mail.WithFileContentType(mail.ContentType(contentType))); err != nil {
			for _, closer := range opened {
				_ = closer.Close()
			}
			return &PermanentError{Err: fmt.Errorf("attaching %q: %w", attachment.Name, err)}
		}
	}
	if err := s.client.DialAndSendWithContext(ctx, msg); err != nil {
		for _, closer := range opened {
			_ = closer.Close()
		}
		return fmt.Errorf("sending email: %w", err)
	}
	for _, closer := range opened {
		_ = closer.Close()
	}
	return nil
}

type robotSender struct {
	kind              model.ChannelType
	webhook           string
	secret            string
	client            *http.Client
	dataDir           string
	appID             string
	appSecret         string
	chatID            string
	feishuTokenCaches *sync.Map
}

func (s *robotSender) Send(ctx context.Context, message Message) error {
	_, err := s.SendProgressive(ctx, message, nil)
	return err
}

func (s *robotSender) SendProgressive(ctx context.Context, message Message, progress *model.DeliveryProgress) (*model.DeliveryProgress, error) {
	current := model.DeliveryProgress{}
	if progress != nil {
		current = *progress
	}
	if s.kind == model.ChannelFeishu {
		updated, err := s.sendFeishuProgressive(ctx, message, &current)
		if businessErr, ok := businessError(err); ok && businessErr.operation == "feishu message" {
			message := feishuActionableMessage(businessErr.code)
			if message == "" {
				return updated, err
			}
			return updated, &ActionableError{
				Message: message,
				Err:     err,
			}
		}
		return updated, err
	}
	var files []model.DeliveryFile
	if s.kind == model.ChannelWeCom {
		message, files = classifyFiles(message, s.dataDir, 5, media.WeComMaxFileSize)
	}
	parts := splitRobotMessage(message, s.kind)
	for index := current.TextPartsSent; index < len(parts); index++ {
		endpoint, payload := s.webhook, any(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": renderMarkdown(parts[index], 4096, true, false)}})
		if s.kind != model.ChannelWeCom {
			var err error
			endpoint, payload, err = s.buildPayload(ctx, parts[index])
			if err != nil {
				return &current, err
			}
		}
		if err := s.postJSON(ctx, endpoint, payload); err != nil {
			return &current, err
		}
		current.TextPartsSent = index + 1
	}
	current.TextSent = current.TextPartsSent == len(parts)
	if s.kind != model.ChannelWeCom {
		return &current, nil
	}
	images := collectLocalImages(message, s.dataDir, media.WeComMaxImageSize)
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
	for i := current.FilesSent; i < len(files); i++ {
		mediaID, err := s.uploadWeComFile(ctx, files[i])
		if err != nil {
			return &current, err
		}
		if err := s.postJSON(ctx, s.webhook, map[string]any{"msgtype": "file", "file": map[string]string{"media_id": mediaID}}); err != nil {
			return &current, err
		}
		current.FilesSent = i + 1
	}
	return &current, nil
}

func feishuActionableMessage(code int64) string {
	switch code {
	case 230002:
		return "飞书机器人不在目标群中。请把当前应用的机器人添加到 Chat ID 对应的群聊，并确认机器人在群内有发言权限。"
	case 230006:
		return "飞书应用尚未启用机器人能力。请在飞书开放平台启用机器人，发布新版本后再测试。"
	case 230013:
		return "目标群或用户不在飞书机器人的可用范围内。请调整应用可用范围，并确认 Chat ID 指向群聊而不是单聊。"
	case 230035, 99991672:
		return "飞书应用缺少发消息权限。请开通“以应用的身份发消息”（im:message:send_as_bot），发布新版本后再测试。"
	default:
		return ""
	}
}

func splitRobotMessage(message Message, kind model.ChannelType) []Message {
	if !message.AllowSplit || len(message.Sections) != 1 || len(message.Sections[0].Paragraphs) != 1 {
		return []Message{message}
	}
	chunks := splitRobotText(message, kind)
	if len(chunks) <= 1 {
		return []Message{message}
	}
	parts := make([]Message, 0, len(chunks))
	for index, chunk := range chunks {
		part := TextMessage(fmt.Sprintf("%s（%d/%d）", message.Subject, index+1, len(chunks)), chunk)
		part.Action = message.Action
		parts = append(parts, part)
	}
	return parts
}

func splitRobotText(message Message, kind model.ChannelType) []string {
	value := message.Sections[0].Paragraphs[0]
	if value == "" || robotMessageFits(message, kind) {
		return []string{value}
	}
	// Probe with a fixed-width worst-case suffix. Actual part numbers are
	// shorter, so chunks accepted here cannot be truncated by the renderer.
	probe := message
	probe.Sections = slices.Clone(message.Sections)
	probe.Sections[0].Paragraphs = slices.Clone(message.Sections[0].Paragraphs)
	probe.Subject = message.Subject + "（999999/999999）"
	parts := make([]string, 0)
	runes := []rune(value)
	for start := 0; start < len(runes); {
		remaining := len(runes) - start
		best, high := 0, 1
		for high <= remaining {
			probe.Sections[0].Paragraphs[0] = string(runes[start : start+high])
			if !robotMessageFits(probe, kind) {
				break
			}
			best = high
			if high == remaining {
				break
			}
			high = min(high*2, remaining)
		}
		low := best + 1
		for low <= high {
			middle := low + (high-low)/2
			probe.Sections[0].Paragraphs[0] = string(runes[start : start+middle])
			if robotMessageFits(probe, kind) {
				best = middle
				low = middle + 1
			} else {
				high = middle - 1
			}
		}
		if best == 0 {
			// AI notification subjects and source URLs are bounded in practice.
			// Still make progress for pathological input instead of looping.
			best = 1
		}
		end := start + best
		for index := end - 1; index > start+best/2; index-- {
			if runes[index] == '\n' {
				end = index + 1
				break
			}
		}
		parts = append(parts, string(runes[start:end]))
		start = end
	}
	return parts
}

func robotMessageFits(message Message, kind model.ChannelType) bool {
	switch kind {
	case model.ChannelWeCom:
		full := renderMarkdown(message, int(^uint(0)>>1), true, false)
		return len(full) <= 4096
	case model.ChannelDingTalk:
		full := renderMarkdown(message, int(^uint(0)>>1), false, true)
		return len([]rune(full)) <= 20_000
	case model.ChannelFeishu:
		rows := [][]feishuElement{{{Tag: "text", Text: message.Sections[0].Paragraphs[0]}}}
		if message.Action.URL != "" {
			rows = append(rows, []feishuElement{{Tag: "a", Text: message.Action.Label, Href: message.Action.URL}})
		}
		return payloadSize(feishuPayload(message.Subject, rows, "9999999999", strings.Repeat("s", 44))) <= 20*1024
	default:
		return true
	}
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

func (s *robotSender) buildPayload(_ context.Context, message Message) (endpoint string, payload any, err error) {
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
		return sanitizedHTTPTransportError(fmt.Sprintf("posting %s notification", s.kind), err)
	}
	defer resp.Body.Close()
	responseBody, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("reading %s response: %w", s.kind, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return retryableHTTPError(string(s.kind), resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &PermanentError{Err: fmt.Errorf("%s returned HTTP %d", s.kind, resp.StatusCode)}
	}
	if oversized {
		return &PermanentError{Err: fmt.Errorf("%s response exceeds %d bytes", s.kind, maxProtocolResponseBytes)}
	}
	var result map[string]any
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return &PermanentError{Err: fmt.Errorf("decoding %s response: %w", s.kind, err)}
	}
	code, err := businessCode(result)
	if err != nil {
		return &PermanentError{Err: fmt.Errorf("decoding %s business result: %w", s.kind, err)}
	}
	if code != 0 {
		return &PermanentError{Err: fmt.Errorf("%s returned business code %d", s.kind, code)}
	}
	return nil
}

type feishuTokenCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

var feishuTokens sync.Map // credential fingerprint -> *feishuTokenCache

func feishuTokenCacheKey(appID, appSecret string) string {
	digest := sha256.Sum256([]byte(appSecret))
	return appID + ":" + hex.EncodeToString(digest[:])
}

func (s *robotSender) feishuTenantToken(ctx context.Context) (string, error) {
	caches := s.feishuTokenCaches
	if caches == nil {
		caches = &feishuTokens
	}
	raw, _ := caches.LoadOrStore(feishuTokenCacheKey(s.appID, s.appSecret), &feishuTokenCache{})
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
		return "", sanitizedHTTPTransportError("requesting feishu tenant token", err)
	}
	defer resp.Body.Close()
	responseBody, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading feishu tenant token: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", retryableHTTPError("feishu tenant token", resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &PermanentError{Err: fmt.Errorf("feishu tenant token returned HTTP %d", resp.StatusCode)}
	}
	if oversized {
		return "", &PermanentError{Err: fmt.Errorf("feishu tenant token response exceeds %d bytes", maxProtocolResponseBytes)}
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
		err := &PermanentError{Err: fmt.Errorf("feishu tenant token failed: code=%d", result.Code)}
		if result.Code == 10014 {
			return "", &ActionableError{
				Message: "飞书拒绝了 App ID 与 App Secret。请从同一个应用的“凭证与基础信息”页面重新复制两项凭证；如果重置过 App Secret，必须填写最新值。",
				Err:     err,
			}
		}
		return "", err
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
		return "", sanitizedHTTPTransportError("uploading feishu image", err)
	}
	defer resp.Body.Close()
	responseBody, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading feishu image upload: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", retryableHTTPError("feishu image upload", resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &PermanentError{Err: fmt.Errorf("feishu image upload returned HTTP %d", resp.StatusCode)}
	}
	if oversized {
		return "", &PermanentError{Err: fmt.Errorf("feishu image upload response exceeds %d bytes", maxProtocolResponseBytes)}
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
		return "", &PermanentError{Err: fmt.Errorf("feishu image upload failed: code=%d", result.Code)}
	}
	return result.Data.ImageKey, nil
}

func (s *robotSender) sendFeishuProgressive(ctx context.Context, message Message, current *model.DeliveryProgress) (*model.DeliveryProgress, error) {
	message, files := classifyFiles(message, s.dataDir, 1, media.FeishuMaxFileSize)
	token, err := s.feishuTenantToken(ctx)
	if err != nil {
		return current, err
	}
	parts := splitRobotMessage(message, model.ChannelFeishu)
	for index := current.TextPartsSent; index < len(parts); index++ {
		keys, err := s.uploadFeishuImagesWithToken(ctx, token, parts[index])
		if err != nil {
			return current, err
		}
		payload := renderFeishuPayload(parts[index], "", "", keys)
		contentContainer, ok := payload["content"].(map[string]any)
		if !ok {
			return current, &PermanentError{Err: errors.New("encoding feishu post: invalid content")}
		}
		content, err := json.Marshal(contentContainer["post"])
		if err != nil {
			return current, &PermanentError{Err: fmt.Errorf("encoding feishu post: %w", err)}
		}
		if err := s.postFeishuMessage(ctx, token, "post", string(content)); err != nil {
			return current, err
		}
		current.TextPartsSent = index + 1
		current.ImagesSent += len(keys)
	}
	current.TextSent = current.TextPartsSent == len(parts)
	for index := current.FilesSent; index < len(files); index++ {
		key, err := s.uploadFeishuFile(ctx, token, files[index])
		if err != nil {
			return current, err
		}
		content, err := json.Marshal(map[string]string{"file_key": key})
		if err != nil {
			return current, &PermanentError{Err: err}
		}
		if err := s.postFeishuMessage(ctx, token, "file", string(content)); err != nil {
			return current, err
		}
		current.FilesSent = index + 1
	}
	return current, nil
}

func (s *robotSender) uploadFeishuImagesWithToken(ctx context.Context, token string, message Message) (map[string]string, error) {
	keys := make(map[string]string)
	for _, section := range message.Sections {
		for _, item := range section.Images {
			if item.LocalPath == "" || keys[item.LocalPath] != "" {
				continue
			}
			data, contentType, err := media.ReadFile(s.dataDir, item.LocalPath)
			if err != nil || len(data) == 0 {
				continue
			}
			key, err := s.uploadFeishuImage(ctx, token, localImage{name: filepath.Base(item.LocalPath), data: data, contentType: firstNonEmpty(item.ContentType, contentType)})
			if err != nil {
				return nil, err
			}
			keys[item.LocalPath] = key
		}
	}
	return keys, nil
}

func (s *robotSender) postFeishuMessage(ctx context.Context, token, msgType, content string) error {
	payload := map[string]string{"receive_id": s.chatID, "msg_type": msgType, "content": content}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: err}
	}
	endpoint := "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doBusinessRequest(ctx, s.client, req, "feishu message", "code")
}

func (s *robotSender) uploadFeishuFile(ctx context.Context, token string, item model.DeliveryFile) (string, error) {
	fields := map[string]string{"file_type": "stream", "file_name": item.Name}
	req, err := multipartFileRequest(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/files", "file", item, s.dataDir, fields)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var result struct {
		Code int `json:"code"`
		Data struct {
			FileKey string `json:"file_key"`
		} `json:"data"`
	}
	if err := doJSONRequest(ctx, s.client, req, "feishu file upload", &result); err != nil {
		return "", err
	}
	if result.Code != 0 || result.Data.FileKey == "" {
		return "", &PermanentError{Err: fmt.Errorf("feishu file upload failed: code=%d", result.Code)}
	}
	return result.Data.FileKey, nil
}

func (s *robotSender) uploadWeComFile(ctx context.Context, item model.DeliveryFile) (string, error) {
	u, err := url.Parse(s.webhook)
	if err != nil {
		return "", &PermanentError{Err: errors.New("invalid WeCom webhook")}
	}
	key := u.Query().Get("key")
	if key == "" {
		return "", &PermanentError{Err: errors.New("WeCom webhook key is required")}
	}
	u.Path = strings.TrimSuffix(u.Path, "/send") + "/upload_media"
	u.RawQuery = url.Values{"key": []string{key}, "type": []string{"file"}}.Encode()
	req, err := multipartFileRequest(ctx, http.MethodPost, u.String(), "media", item, s.dataDir, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		ErrCode int    `json:"errcode"`
		MediaID string `json:"media_id"`
	}
	if err := doJSONRequest(ctx, s.client, req, "wecom file upload", &result); err != nil {
		return "", err
	}
	if result.ErrCode != 0 || result.MediaID == "" {
		return "", &PermanentError{Err: fmt.Errorf("wecom file upload failed: code=%d", result.ErrCode)}
	}
	return result.MediaID, nil
}

func multipartFileRequest(ctx context.Context, method, endpoint, field string, item model.DeliveryFile, dataDir string, fields map[string]string) (*http.Request, error) {
	file, size, _, err := media.OpenFile(dataDir, item.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("opening attachment %q: %w", item.Name, err)
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		var writeErr error
		for key, value := range fields {
			if writeErr == nil {
				writeErr = multipartWriter.WriteField(key, value)
			}
		}
		if writeErr == nil {
			var part io.Writer
			part, writeErr = multipartWriter.CreateFormFile(field, safeAttachmentName(item.Name))
			if writeErr == nil {
				_, writeErr = io.Copy(part, file)
			}
		}
		_ = file.Close()
		if closeErr := multipartWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
	}()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		_ = reader.Close()
		return nil, &PermanentError{Err: err}
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	if overhead, err := multipartBodyOverhead(multipartWriter.Boundary(), field, safeAttachmentName(item.Name), fields); err == nil {
		req.ContentLength = size + overhead
	}
	return req, nil
}

func multipartBodyOverhead(boundary, field, name string, fields map[string]string) (int64, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.SetBoundary(boundary); err != nil {
		return 0, err
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return 0, err
		}
	}
	if _, err := writer.CreateFormFile(field, name); err != nil {
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, err
	}
	return int64(buffer.Len()), nil
}

func doBusinessRequest(ctx context.Context, client *http.Client, req *http.Request, operation, codeKey string) error {
	var result map[string]any
	if err := doJSONRequest(ctx, client, req, operation, &result); err != nil {
		return err
	}
	value, ok := result[codeKey]
	if !ok {
		return &PermanentError{Err: fmt.Errorf("%s response is missing business code", operation)}
	}
	code, ok := value.(float64)
	if !ok || code != float64(int64(code)) {
		return &PermanentError{Err: fmt.Errorf("%s returned invalid business code", operation)}
	}
	if code != 0 {
		return &PermanentError{Err: &upstreamBusinessError{operation: operation, code: int64(code)}}
	}
	return nil
}

func doJSONRequest(ctx context.Context, client *http.Client, req *http.Request, operation string, target any) error {
	resp, err := client.Do(req)
	if err != nil {
		return sanitizedHTTPTransportError(operation, err)
	}
	defer resp.Body.Close()
	body, oversized, err := readProtocolResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("reading %s response: %w", operation, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return retryableHTTPError(operation, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if !oversized {
			var result map[string]any
			if json.Unmarshal(body, &result) == nil {
				if code, codeErr := businessCode(result); codeErr == nil && code != 0 {
					return &PermanentError{Err: &upstreamBusinessError{operation: operation, statusCode: resp.StatusCode, code: code}}
				}
			}
		}
		return &PermanentError{Err: fmt.Errorf("%s returned HTTP %d", operation, resp.StatusCode)}
	}
	if oversized {
		return &PermanentError{Err: fmt.Errorf("%s response exceeds %d bytes", operation, maxProtocolResponseBytes)}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return &PermanentError{Err: fmt.Errorf("decoding %s response: %w", operation, err)}
	}
	return nil
}

func readProtocolResponse(reader io.Reader) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxProtocolResponseBytes+1))
	if err != nil {
		return nil, false, err
	}
	return body, len(body) > maxProtocolResponseBytes, nil
}

func retryableHTTPError(operation string, response *http.Response) error {
	err := fmt.Errorf("%s returned HTTP %d", operation, response.StatusCode)
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return err
	}
	if seconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil && seconds > 0 {
		return &RetryAfterError{Err: err, Delay: time.Duration(seconds) * time.Second}
	}
	if when, parseErr := http.ParseTime(value); parseErr == nil {
		if delay := time.Until(when); delay > 0 {
			return &RetryAfterError{Err: err, Delay: delay}
		}
	}
	return err
}

func sanitizedHTTPTransportError(operation string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return fmt.Errorf("%s: %w", operation, err)
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

func businessCode(result map[string]any) (int64, error) {
	for _, key := range []string{"errcode", "code", "StatusCode"} {
		value, ok := result[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			if v != float64(int64(v)) {
				return 0, fmt.Errorf("field %s is not an integer", key)
			}
			return int64(v), nil
		case string:
			code, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("field %s is not an integer", key)
			}
			return code, nil
		default:
			return 0, fmt.Errorf("field %s has type %T", key, value)
		}
	}
	return 0, errors.New("missing business code")
}

func hmacBase64(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

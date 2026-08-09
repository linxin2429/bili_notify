package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeComSender(t *testing.T) {
	t.Parallel()
	requests := make(chan handlerResult[map[string]any], 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := decodeJSONRequest[map[string]any](r)
		requests <- result
		if result.err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(server.Close)

	sender, err := NewSender(model.Channel{
		Name: "wecom", Type: model.ChannelWeCom,
		Settings: map[string]string{"webhook": server.URL},
	}, server.Client(), "", nil)
	require.NoError(t, err)
	sendErr := sender.Send(t.Context(), TextMessage("s", "hello"))
	got := requireHandlerResult(t, requests)
	require.NoError(t, sendErr)
	assert.Equal(t, "markdown", got["msgtype"])
}

func TestDynamicMessageRendersRichContent(t *testing.T) {
	t.Parallel()
	dynamic := model.Dynamic{
		ID: "10", UID: "42", UPName: "tester", Type: "DYNAMIC_TYPE_AV",
		PublishedAt: time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC),
		Summary:     "正文 <script>alert(1)</script>",
		URL:         "https://t.bilibili.com/10",
		Title:       "视频标题",
		Description: "视频简介",
		TargetURL:   "https://www.bilibili.com/video/BV1",
		Links:       []model.DynamicLink{{Text: "话题", URL: "https://www.bilibili.com/v/topic/detail"}},
		Media:       []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://i0.hdslb.com/cover.jpg"}},
		Stats:       &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3},
		Video:       &model.DynamicVideo{Duration: "03:21", Views: "1.2万", Danmaku: "88"},
		Original: &model.Dynamic{
			ID: "9", UID: "7", UPName: "author", Type: "DYNAMIC_TYPE_WORD",
			PublishedAt: time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC), Summary: "原动态正文", URL: "https://t.bilibili.com/9",
		},
	}
	message := DynamicMessage(dynamic)
	plain := renderPlainText(message)
	for _, expected := range []string{"视频标题", "视频简介", "播放：1.2万", "原动态正文", "https://t.bilibili.com/10"} {
		assert.Contains(t, plain, expected)
	}
	htmlBody := renderHTML(message)
	assert.NotContains(t, htmlBody, "<script>")
	assert.Contains(t, htmlBody, "&lt;script&gt;")
	assert.Contains(t, htmlBody, `<img src="https://i0.hdslb.com/cover.jpg"`)
	markdown := renderMarkdown(message, 20_000, false, true)
	assert.Contains(t, markdown, `![封面](https://i0.hdslb.com/cover.jpg)`)
	assert.Contains(t, markdown, "转发自 author")
}

func TestDynamicTypeName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		dynamicType string
		want        string
	}{
		{name: "word", dynamicType: "DYNAMIC_TYPE_WORD", want: "文字动态"},
		{name: "draw", dynamicType: "DYNAMIC_TYPE_DRAW", want: "图片动态"},
		{name: "video", dynamicType: "DYNAMIC_TYPE_AV", want: "视频投稿"},
		{name: "article", dynamicType: "DYNAMIC_TYPE_ARTICLE", want: "专栏"},
		{name: "forward", dynamicType: "DYNAMIC_TYPE_FORWARD", want: "转发动态"},
		{name: "pgc", dynamicType: "DYNAMIC_TYPE_PGC", want: "番剧内容"},
		{name: "common", dynamicType: "DYNAMIC_TYPE_COMMON_SQUARE", want: "动态"},
		{name: "unknown", dynamicType: "DYNAMIC_TYPE_UNKNOWN", want: "DYNAMIC_TYPE_UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dynamicTypeName(tt.dynamicType))
		})
	}
}

func TestMarkdownLengthLimitsPreserveUTF8AndSourceLink(t *testing.T) {
	t.Parallel()
	message := Message{
		Subject:  "长动态",
		Sections: []Section{{Paragraphs: []string{strings.Repeat("正文🙂", 3000)}, Images: []Image{{Label: "图片", URL: "https://i0.hdslb.com/image.jpg"}}}},
		Action:   Link{Label: "查看原动态", URL: "https://t.bilibili.com/1"},
	}
	wecom := renderMarkdown(message, 4096, true, false)
	assert.LessOrEqual(t, len(wecom), 4096)
	assert.True(t, utf8.ValidString(wecom))
	assert.Contains(t, wecom, "内容已截断")
	assert.Contains(t, wecom, message.Action.URL)
	assert.Contains(t, wecom, message.Sections[0].Images[0].URL)

	dingTalk := renderMarkdown(message, 20_000, false, true)
	assert.LessOrEqual(t, len([]rune(dingTalk)), 20_000)
	assert.True(t, utf8.ValidString(dingTalk))
	assert.Contains(t, dingTalk, message.Action.URL)
}

func TestFeishuPostIsRichTextAndWithinLimit(t *testing.T) {
	t.Parallel()
	message := Message{
		Subject:  "长动态",
		Sections: []Section{{Paragraphs: []string{strings.Repeat("正文🙂", 7000)}, Images: []Image{{Label: "图片", URL: "https://i0.hdslb.com/image.jpg"}}}},
		Action:   Link{Label: "查看原动态", URL: "https://t.bilibili.com/1"},
	}
	payload := renderFeishuPayload(message, "1", "sign", nil)
	assert.Equal(t, "post", payload["msg_type"])
	assert.LessOrEqual(t, payloadSize(payload), 20*1024)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	for _, expected := range []string{"内容已截断", "https://i0.hdslb.com/image.jpg", "https://t.bilibili.com/1"} {
		assert.Contains(t, string(raw), expected)
	}
}

func TestRobotSenderUsesChannelSpecificFormats(t *testing.T) {
	t.Parallel()
	for _, channelType := range []model.ChannelType{model.ChannelDingTalk, model.ChannelFeishu} {
		t.Run(string(channelType), func(t *testing.T) {
			t.Parallel()
			requests := make(chan handlerResult[map[string]any], 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				result := decodeJSONRequest[map[string]any](r)
				requests <- result
				if result.err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"errcode":0,"code":0}`))
			}))
			t.Cleanup(server.Close)
			sender, err := NewSender(model.Channel{
				Name: string(channelType), Type: channelType,
				Settings: map[string]string{"webhook": server.URL, "secret": "secret"},
			}, server.Client(), "", nil)
			require.NoError(t, err)
			message := Message{
				Subject: "subject", Sections: []Section{{Paragraphs: []string{"body"}, Images: []Image{{Label: "image", URL: "https://example.com/image.jpg"}}}},
			}
			sendErr := sender.Send(t.Context(), message)
			got := requireHandlerResult(t, requests)
			require.NoError(t, sendErr)
			switch channelType {
			case model.ChannelDingTalk:
				assert.Equal(t, "markdown", got["msgtype"])
			case model.ChannelFeishu:
				assert.Equal(t, "post", got["msg_type"])
			}
		})
	}
}

func TestMicrosoftSenderRefreshesTokenAndSendsGraphMail(t *testing.T) {
	t.Parallel()
	tokenRequests := make(chan handlerResult[url.Values], 1)
	type graphRequest struct {
		authorization string
		payload       map[string]any
	}
	graphRequests := make(chan handlerResult[graphRequest], 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			result := parseFormRequest(r)
			tokenRequests <- result
			if result.err != nil {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
		case "/send":
			decoded := decodeJSONRequest[map[string]any](r)
			graphRequests <- handlerResult[graphRequest]{
				value: graphRequest{authorization: r.Header.Get("Authorization"), payload: decoded.value},
				err:   decoded.err,
			}
			if decoded.err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	settings := map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common",
		"to": "one@example.com,Two <two@example.com>", "access_token": "old-access",
		"refresh_token": "old-refresh", "token_type": "Bearer",
		"token_expiry": time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
	}
	var updated map[string]string
	sender := newMicrosoftSender(settings, server.Client(), "", func(values map[string]string) error {
		updated = values
		return nil
	}, microsoftEndpoints{tokenURL: server.URL + "/token", graphSendURL: server.URL + "/send"})
	sendErr := sender.Send(t.Context(), TextMessage("subject", "body"))
	tokenRequest := requireHandlerResult(t, tokenRequests)
	graph := requireHandlerResult(t, graphRequests)
	require.NoError(t, sendErr)
	assert.Equal(t, "old-refresh", tokenRequest.Get("refresh_token"))
	assert.Equal(t, "Bearer new-access", graph.authorization)
	assert.Equal(t, "new-refresh", updated["refresh_token"])
	assert.Equal(t, "true", updated["authorized"])
	message, ok := graph.payload["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "subject", message["subject"])
	recipients, ok := message["toRecipients"].([]any)
	require.True(t, ok)
	assert.Len(t, recipients, 2)
}

func TestStartMicrosoftDeviceAuth(t *testing.T) {
	t.Parallel()
	requests := make(chan handlerResult[url.Values], 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			http.NotFound(w, r)
			return
		}
		result := parseFormRequest(r)
		requests <- result
		if result.err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":5}`))
	}))
	t.Cleanup(server.Close)

	auth, err := startMicrosoftDeviceAuth(t.Context(), map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555",
	}, server.Client(), microsoftEndpoints{deviceAuthURL: server.URL + "/device", tokenURL: server.URL + "/token"})
	request := requireHandlerResult(t, requests)
	require.NoError(t, err)
	assert.Contains(t, request.Get("scope"), "Mail.Send")
	assert.Equal(t, "ABCD-EFGH", auth.UserCode)
	assert.Equal(t, "https://microsoft.com/devicelogin", auth.VerificationURI)
}

func TestCommentThreadMessage(t *testing.T) {
	t.Parallel()
	note := model.CommentNotification{
		RPID: "r2", UPUID: "42", UPName: "tester", ContentType: "DYNAMIC_TYPE_AV",
		ContentID: "10", ContentTitle: "视频标题", ContentURL: "https://www.bilibili.com/video/BV1",
		PublishedAt: time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC),
		Thread: []model.CommentNode{
			{RPID: "r0", Name: "fan", Message: "求更新", Time: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)},
			{RPID: "r1", Parent: "r0", Name: "other", Message: "同求", Time: time.Date(2026, 8, 4, 0, 30, 0, 0, time.UTC)},
			{RPID: "r2", Parent: "r1", Name: "tester", Message: "下周", IsUP: true, IsTrigger: true, Time: time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)},
		},
	}
	message := CommentThreadMessage(note)
	assert.Equal(t, "[B站评论] tester 回复了评论", message.Subject)
	plain := renderPlainText(message)
	for _, expected := range []string{"视频标题", "fan：求更新", "↳ other：同求", "tester（UP） ★：下周", "https://www.bilibili.com/video/BV1"} {
		assert.Contains(t, plain, expected)
	}
	markdown := renderMarkdown(message, 4096, true, false)
	assert.Contains(t, markdown, "tester（UP）")
	assert.True(t, utf8.ValidString(markdown))
}

func TestWeComProgressiveSendResumes(t *testing.T) {
	t.Parallel()
	requests := make(chan handlerResult[map[string]any], 3)
	var requestCount atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := decodeJSONRequest[map[string]any](r)
		requests <- result
		if result.err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if requestCount.Add(1) == 2 {
			_, _ = w.Write([]byte(`{"errcode":1,"errmsg":"temp"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	rel := filepath.Join("media", "1", "2", "0.png")
	abs := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o700))
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	require.NoError(t, os.WriteFile(abs, png, 0o600))

	sender, err := NewSender(model.Channel{
		Name: "wecom", Type: model.ChannelWeCom,
		Settings: map[string]string{"webhook": server.URL},
	}, server.Client(), dir, nil)
	require.NoError(t, err)
	progressive, ok := sender.(ProgressiveSender)
	require.True(t, ok)

	message := Message{
		Subject: "s",
		Sections: []Section{{
			Paragraphs: []string{"body"},
			Images:     []Image{{Label: "图片", URL: "https://example.com/a.png", LocalPath: filepath.ToSlash(rel)}},
		}},
	}
	progress, err := progressive.SendProgressive(t.Context(), message, nil)
	firstPayload := requireHandlerResult(t, requests)
	secondPayload := requireHandlerResult(t, requests)
	require.Error(t, err)
	require.NotNil(t, progress)
	assert.True(t, progress.TextSent)
	assert.Equal(t, 0, progress.ImagesSent)
	assert.Equal(t, "markdown", firstPayload["msgtype"])
	assert.Equal(t, "image", secondPayload["msgtype"])

	progress, err = progressive.SendProgressive(t.Context(), message, progress)
	thirdPayload := requireHandlerResult(t, requests)
	require.NoError(t, err)
	require.NotNil(t, progress)
	assert.True(t, progress.TextSent)
	assert.Equal(t, 1, progress.ImagesSent)
	assert.Equal(t, "image", thirdPayload["msgtype"])
}

type handlerResult[T any] struct {
	value T
	err   error
}

func decodeJSONRequest[T any](request *http.Request) handlerResult[T] {
	var value T
	err := json.NewDecoder(request.Body).Decode(&value)
	return handlerResult[T]{value: value, err: err}
}

func parseFormRequest(request *http.Request) handlerResult[url.Values] {
	err := request.ParseForm()
	return handlerResult[url.Values]{value: request.Form, err: err}
}

func requireHandlerResult[T any](t *testing.T, results <-chan handlerResult[T]) T {
	t.Helper()
	select {
	case result := <-results:
		require.NoError(t, result.err)
		return result.value
	case <-time.After(5 * time.Second):
		require.FailNow(t, "expected request was not received by the test server")
		var zero T
		return zero
	}
}

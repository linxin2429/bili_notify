package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/linxin2429/bili_notify/model"
)

func TestWeComSender(t *testing.T) {
	var got map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	sender, err := NewSender(model.Channel{
		Name: "wecom", Type: model.ChannelWeCom,
		Settings: map[string]string{"webhook": server.URL},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), TextMessage("s", "hello")); err != nil {
		t.Fatal(err)
	}
	if got["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v", got["msgtype"])
	}
}

func TestDynamicMessageRendersRichContent(t *testing.T) {
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
		if !strings.Contains(plain, expected) {
			t.Fatalf("plain text does not contain %q:\n%s", expected, plain)
		}
	}
	htmlBody := renderHTML(message)
	if strings.Contains(htmlBody, "<script>") || !strings.Contains(htmlBody, "&lt;script&gt;") || !strings.Contains(htmlBody, `<img src="https://i0.hdslb.com/cover.jpg"`) {
		t.Fatalf("HTML = %s", htmlBody)
	}
	markdown := renderMarkdown(message, 20_000, false, true)
	if !strings.Contains(markdown, `![封面](https://i0.hdslb.com/cover.jpg)`) || !strings.Contains(markdown, "转发自 author") {
		t.Fatalf("Markdown = %s", markdown)
	}
}

func TestMarkdownLengthLimitsPreserveUTF8AndSourceLink(t *testing.T) {
	message := Message{
		Subject:  "长动态",
		Sections: []Section{{Paragraphs: []string{strings.Repeat("正文🙂", 3000)}, Images: []Image{{Label: "图片", URL: "https://i0.hdslb.com/image.jpg"}}}},
		Action:   Link{Label: "查看原动态", URL: "https://t.bilibili.com/1"},
	}
	wecom := renderMarkdown(message, 4096, true, false)
	if len(wecom) > 4096 || !utf8.ValidString(wecom) || !strings.Contains(wecom, "内容已截断") || !strings.Contains(wecom, message.Action.URL) || !strings.Contains(wecom, message.Sections[0].Images[0].URL) {
		t.Fatalf("WeCom markdown bytes=%d valid=%v:\n%s", len(wecom), utf8.ValidString(wecom), wecom)
	}
	dingTalk := renderMarkdown(message, 20_000, false, true)
	if len([]rune(dingTalk)) > 20_000 || !utf8.ValidString(dingTalk) || !strings.Contains(dingTalk, message.Action.URL) {
		t.Fatalf("DingTalk markdown runes=%d", len([]rune(dingTalk)))
	}
}

func TestFeishuPostIsRichTextAndWithinLimit(t *testing.T) {
	message := Message{
		Subject:  "长动态",
		Sections: []Section{{Paragraphs: []string{strings.Repeat("正文🙂", 7000)}, Images: []Image{{Label: "图片", URL: "https://i0.hdslb.com/image.jpg"}}}},
		Action:   Link{Label: "查看原动态", URL: "https://t.bilibili.com/1"},
	}
	payload := renderFeishuPayload(message, "1", "sign")
	if payload["msg_type"] != "post" || payloadSize(payload) > 20*1024 {
		t.Fatalf("payload type=%v size=%d", payload["msg_type"], payloadSize(payload))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"内容已截断", "https://i0.hdslb.com/image.jpg", "https://t.bilibili.com/1"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("payload does not contain %q: %s", expected, raw)
		}
	}
}

func TestRobotSenderUsesChannelSpecificFormats(t *testing.T) {
	for _, channelType := range []model.ChannelType{model.ChannelDingTalk, model.ChannelFeishu} {
		t.Run(string(channelType), func(t *testing.T) {
			var got map[string]any
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{"errcode":0,"code":0}`))
			}))
			defer server.Close()
			sender, err := NewSender(model.Channel{
				Name: string(channelType), Type: channelType,
				Settings: map[string]string{"webhook": server.URL, "secret": "secret"},
			}, server.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			message := Message{
				Subject: "subject", Sections: []Section{{Paragraphs: []string{"body"}, Images: []Image{{Label: "image", URL: "https://example.com/image.jpg"}}}},
			}
			if err := sender.Send(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			if channelType == model.ChannelDingTalk && got["msgtype"] != "markdown" {
				t.Fatalf("DingTalk payload = %#v", got)
			}
			if channelType == model.ChannelFeishu && got["msg_type"] != "post" {
				t.Fatalf("Feishu payload = %#v", got)
			}
		})
	}
}

func TestMicrosoftSenderRefreshesTokenAndSendsGraphMail(t *testing.T) {
	var gotAuthorization string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
		case "/send":
			gotAuthorization = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common",
		"to": "one@example.com,Two <two@example.com>", "access_token": "old-access",
		"refresh_token": "old-refresh", "token_type": "Bearer",
		"token_expiry": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
	}
	var updated map[string]string
	sender := newMicrosoftSender(settings, server.Client(), func(values map[string]string) error {
		updated = values
		return nil
	}, microsoftEndpoints{tokenURL: server.URL + "/token", graphSendURL: server.URL + "/send"})
	if err := sender.Send(t.Context(), TextMessage("subject", "body")); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer new-access" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if updated["refresh_token"] != "new-refresh" || updated["authorized"] != "true" {
		t.Fatalf("updated settings = %#v", updated)
	}
	message, ok := gotPayload["message"].(map[string]any)
	if !ok || message["subject"] != "subject" {
		t.Fatalf("payload = %#v", gotPayload)
	}
	recipients, ok := message["toRecipients"].([]any)
	if !ok || len(recipients) != 2 {
		t.Fatalf("recipients = %#v", message["toRecipients"])
	}
}

func TestStartMicrosoftDeviceAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Form.Get("scope"), "Mail.Send") {
			t.Errorf("scope = %q", r.Form.Get("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":5}`))
	}))
	defer server.Close()

	auth, err := startMicrosoftDeviceAuth(t.Context(), map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555",
	}, server.Client(), microsoftEndpoints{deviceAuthURL: server.URL + "/device", tokenURL: server.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode != "ABCD-EFGH" || auth.VerificationURI != "https://microsoft.com/devicelogin" {
		t.Fatalf("authorization = %#v", auth)
	}
}

package notify

import (
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAINotificationMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		value       model.AINotification
		wantSplit   bool
		wantBody    string
		wantSubject string
		wantAction  string
	}{
		{name: "successful transcription", value: model.AINotification{Stage: model.AIJobTranscription, Succeeded: true, Title: "video", Body: "transcript", SourceURL: "https://www.bilibili.com/video/BV1xx"}, wantSplit: true, wantBody: "transcript", wantSubject: "视频转写完成", wantAction: "https://www.bilibili.com/video/BV1xx"},
		{name: "failed summary", value: model.AINotification{Stage: model.AIJobSummary, ErrorCode: "provider_error", ErrorMessage: "failed", Title: "video"}, wantBody: "provider_error：failed", wantSubject: "视频总结失败"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message := AINotificationMessage(tt.value)
			assert.Equal(t, tt.wantSplit, message.AllowSplit)
			assert.Contains(t, message.Subject, tt.wantSubject)
			assert.Equal(t, tt.wantAction, message.Action.URL)
			require.Len(t, message.Sections, 1)
			require.Len(t, message.Sections[0].Paragraphs, 1)
			assert.Equal(t, tt.wantBody, message.Sections[0].Paragraphs[0])
		})
	}
}

func TestSplitRobotMessagePreservesFullTranscript(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		kind      model.ChannelType
		body      string
		wantParts int
	}{
		{name: "wecom byte limit", kind: model.ChannelWeCom, body: strings.Repeat("中", 2000), wantParts: 2},
		{name: "wecom markdown escaping", kind: model.ChannelWeCom, body: strings.Repeat("*_[]`\\", 800), wantParts: 3},
		{name: "dingtalk rune limit", kind: model.ChannelDingTalk, body: strings.Repeat("a", 22000), wantParts: 2},
		{name: "whitespace at split boundary", kind: model.ChannelWeCom, body: strings.Repeat("a", 4300) + " \n tail ", wantParts: 3},
		{name: "feishu JSON escaping", kind: model.ChannelFeishu, body: strings.Repeat("\"\\\n", 7000), wantParts: 3},
		{name: "short transcript", kind: model.ChannelFeishu, body: "short", wantParts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message := TextMessage("transcript", tt.body)
			message.AllowSplit = true
			message.Action = Link{Label: "source", URL: "https://www.bilibili.com/video/BV1xx"}
			parts := splitRobotMessage(message, tt.kind)
			assert.Equal(t, tt.body, message.Sections[0].Paragraphs[0])
			require.Len(t, parts, tt.wantParts)
			var joined strings.Builder
			for _, part := range parts {
				require.Len(t, part.Sections, 1)
				require.Len(t, part.Sections[0].Paragraphs, 1)
				joined.WriteString(part.Sections[0].Paragraphs[0])
				assert.True(t, robotMessageFits(part, tt.kind))
			}
			assert.Equal(t, tt.body, joined.String())
		})
	}
}

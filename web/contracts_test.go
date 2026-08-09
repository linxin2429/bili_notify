package web

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRESTResourceContracts(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	fixed := contractTime()
	require.NoError(t, fixture.store.PutUP(contractUP(fixed)))
	_, err := fixture.store.PutChannel(model.Channel{
		ID: "contract-channel", Name: "Contract robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/webhook"}, CreatedAt: fixed,
	})
	require.NoError(t, err)

	response := fixture.request(t, http.MethodGet, "/api/v2/runtime", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var runtime runtimeView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &runtime))
	assert.NotEmpty(t, runtime.Timezone)
	assert.False(t, runtime.UpdatedAt.IsZero())

	response = fixture.request(t, http.MethodGet, "/api/v2/ups", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var ups []model.UP
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &ups))
	require.Len(t, ups, 1)
	assert.Equal(t, "42", ups[0].UID)

	response = fixture.request(t, http.MethodGet, "/api/v2/channels", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var channels []channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &channels))
	require.Len(t, channels, 1)
	assert.Equal(t, []string{"webhook"}, channels[0].ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), "https://example.com/webhook")

	response = fixture.request(t, http.MethodGet, "/api/v2/deliveries", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"items":[],"page":{"next_cursor":"","has_more":false}}`, response.Body.String())
}

func TestWebSocketEventContract(t *testing.T) {
	t.Parallel()
	events := []wsEvent{
		{Event: "sync.required", Revision: 7, Topics: allResourceTopics()},
		{Event: "resources.invalidated", Revision: 8, Topics: []string{"runtime", "ups", "deliveries"}},
	}

	raw, err := json.Marshal(events)
	require.NoError(t, err)
	var got any
	require.NoError(t, json.Unmarshal(raw, &got))
	want := readContractJSON(t, "testdata/contracts/websocket-events.json")
	assert.Equal(t, want, got)
}

func TestRESTContentContracts(t *testing.T) {
	t.Parallel()
	fixed := contractTime()
	responses := map[string]any{
		"dynamics": cursorPageResponse{Items: []dynamicHistoryView{{
			ID: "dynamic-1", UID: "42", UPName: "Contract UP", Type: "DYNAMIC_TYPE_AV",
			PublishedAt: fixed, DiscoveredAt: fixed.Add(time.Minute), Title: "Contract video",
			Summary: "Contract summary", URL: "https://t.bilibili.com/1",
			Media:    []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://example.com/cover.jpg", Width: 1280, Height: 720}},
			Stats:    &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3},
			Video:    &model.DynamicVideo{Duration: "01:23", Views: "456", Danmaku: "7"},
			Original: &state.DynamicPreview{ID: "original-1", Summary: "Original summary", URL: "https://t.bilibili.com/0"},
		}}, Page: cursorPage{}},
		"comments": cursorPageResponse{Items: []commentHistoryView{{
			RPID: "reply-1", UPUID: "42", UPName: "Contract UP", ContentType: "video", ContentID: "BV1contract",
			ContentTitle: "Contract video", ContentURL: "https://www.bilibili.com/video/BV1contract",
			PublishedAt: fixed, DiscoveredAt: fixed.Add(time.Minute),
		}}, Page: cursorPage{}},
		"comment_detail": model.CommentNotification{
			RPID: "reply-1", UPUID: "42", UPName: "Contract UP", ContentType: "video", ContentID: "BV1contract",
			ContentTitle: "Contract video", ContentURL: "https://www.bilibili.com/video/BV1contract", PublishedAt: fixed,
			Thread: []model.CommentNode{
				{RPID: "root-1", Mid: "100", Name: "Viewer", Message: "Question", Time: fixed.Add(-time.Minute)},
				{RPID: "reply-1", Parent: "root-1", Mid: "42", Name: "Contract UP", Message: "Answer", Time: fixed, IsUP: true, IsTrigger: true},
			},
		},
		"audit_logs": cursorPageResponse{Items: []state.AuditLog{{
			ID: 1, OccurredAt: fixed, RequestID: "request-1", Actor: "administrator", SessionID: "session-1",
			RemoteIP: "192.0.2.1", UserAgent: "contract-test", Action: "up.create", ResourceType: "up", ResourceID: "42",
			Outcome: state.AuditSuccess, HTTPMethod: http.MethodPost, Route: "/api/v2/ups", StatusCode: http.StatusCreated,
			DurationMS: 5, Details: map[string]any{"enabled": true},
		}}, Page: cursorPage{}},
	}

	raw, err := json.Marshal(responses)
	require.NoError(t, err)
	var got any
	require.NoError(t, json.Unmarshal(raw, &got))
	want := readContractJSON(t, "testdata/contracts/content-responses.json")
	assert.Equal(t, want, got)
}

func contractUP(fixed time.Time) model.UP {
	return model.UP{
		UID: "42", Name: "Contract UP", Enabled: true, BaselineReady: true, FollowState: model.Followed,
		FollowCheckedAt: fixed, CollectionRoute: model.CollectionRouteFeedAll, LastPollAt: fixed, LastSuccessAt: fixed,
	}
}

func contractTime() time.Time {
	return time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC)
}

func readContractJSON(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

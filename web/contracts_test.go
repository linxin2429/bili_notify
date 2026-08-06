package web

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRESTDashboardContract(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	fixed := contractTime()
	require.NoError(t, fixture.store.PutUP(contractUP(fixed)))
	_, err := fixture.store.PutChannel(model.Channel{
		ID: "contract-channel", Name: "Contract robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/webhook"}, CreatedAt: fixed,
	})
	require.NoError(t, err)

	response := fixture.request(t, http.MethodGet, "/api/v1/dashboard", nil, false)
	require.Equal(t, http.StatusOK, response.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	got["timezone"] = "UTC"
	got["updated_at"] = fixed.Format(time.RFC3339)
	channels, ok := got["channels"].([]any)
	require.True(t, ok)
	require.Len(t, channels, 1)
	channel, ok := channels[0].(map[string]any)
	require.True(t, ok)
	channel["updated_at"] = fixed.Format(time.RFC3339)
	ups, ok := got["ups"].([]any)
	require.True(t, ok)
	require.Len(t, ups, 1)
	up, ok := ups[0].(map[string]any)
	require.True(t, ok)
	up["last_poll_at"] = fixed.Format(time.RFC3339)
	up["last_success_at"] = fixed.Format(time.RFC3339)

	want := readContractJSON(t, "testdata/contracts/dashboard.json")
	assert.Equal(t, want, got)
}

func TestWebSocketEventContract(t *testing.T) {
	t.Parallel()
	fixed := contractTime()
	settings := webTestSettings()
	up := contractUP(fixed)
	channel := channelView{
		ID: "contract-channel", Name: "Contract robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{}, ConfiguredSecrets: []string{"webhook"}, CreatedAt: fixed, UpdatedAt: fixed,
	}
	status := service.Status{
		AuthValid: true, BiliAccount: &model.BiliAccount{UID: "100", Name: "Contract account"}, LastSuccessAt: fixed,
		UPCount: 1, ChannelCount: 1, OutboxDepth: 1, OldestDelivery: fixed, Ready: true,
	}
	events := []wsEvent{
		{Event: "snapshot", Revision: 7, Data: dashboardSnapshot{
			Status: service.Status{UPCount: 1, ChannelCount: 1}, Settings: settings, UPs: []model.UP{up},
			Channels: []channelView{channel}, Deliveries: []deliveryView{}, MicrosoftLogins: []service.MicrosoftLoginSession{},
			Timezone: "UTC", UpdatedAt: fixed,
		}},
		{Event: "status.updated", Revision: 8, Data: status},
		{Event: "settings.updated", Revision: 9, Data: settings},
		{Event: "ups.updated", Revision: 10, Data: []model.UP{up}},
		{Event: "channels.updated", Revision: 11, Data: []channelView{channel}},
		{Event: "deliveries.updated", Revision: 12, Data: []deliveryView{{
			ID: "dynamic-1:contract-channel", Kind: model.DeliveryKindDynamic,
			Dynamic:   dynamicPreview{ID: "dynamic-1", UID: "42", UPName: "Contract UP", Type: "DYNAMIC_TYPE_WORD", PublishedAt: fixed, Summary: "Contract dynamic", URL: "https://t.bilibili.com/1"},
			ChannelID: "contract-channel", State: model.DeliveryPending, NextAt: fixed, CreatedAt: fixed,
		}}},
		{Event: "bilibili.login.updated", Revision: 13, Data: biliLoginView{ID: "login-1", Status: "waiting", ExpiresAt: fixed.Add(5 * time.Minute), QRDataURL: "data:image/png;base64,Y29udHJhY3Q="}},
		{Event: "microsoft.login.updated", Revision: 14, Data: []service.MicrosoftLoginSession{{
			ChannelID: "contract-channel", Status: "waiting", UserCode: "ABCD-EFGH",
			VerificationURI: "https://microsoft.com/devicelogin", ExpiresAt: fixed.Add(15 * time.Minute),
		}}},
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
		"dynamics": contentPage{Items: []dynamicHistoryView{{
			ID: "dynamic-1", UID: "42", UPName: "Contract UP", Type: "DYNAMIC_TYPE_AV",
			PublishedAt: fixed, DiscoveredAt: fixed.Add(time.Minute), Title: "Contract video",
			Summary: "Contract summary", URL: "https://t.bilibili.com/1",
			Media:    []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://example.com/cover.jpg", Width: 1280, Height: 720}},
			Stats:    &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3},
			Video:    &model.DynamicVideo{Duration: "01:23", Views: "456", Danmaku: "7"},
			Original: &state.DynamicPreview{ID: "original-1", Summary: "Original summary", URL: "https://t.bilibili.com/0"},
		}}, Total: 1, Limit: 20},
		"comments": contentPage{Items: []commentHistoryView{{
			RPID: "reply-1", UPUID: "42", UPName: "Contract UP", ContentType: "video", ContentID: "BV1contract",
			ContentTitle: "Contract video", ContentURL: "https://www.bilibili.com/video/BV1contract",
			PublishedAt: fixed, DiscoveredAt: fixed.Add(time.Minute),
		}}, Total: 1, Limit: 20},
		"comment_detail": model.CommentNotification{
			RPID: "reply-1", UPUID: "42", UPName: "Contract UP", ContentType: "video", ContentID: "BV1contract",
			ContentTitle: "Contract video", ContentURL: "https://www.bilibili.com/video/BV1contract", PublishedAt: fixed,
			Thread: []model.CommentNode{
				{RPID: "root-1", Mid: "100", Name: "Viewer", Message: "Question", Time: fixed.Add(-time.Minute)},
				{RPID: "reply-1", Parent: "root-1", Mid: "42", Name: "Contract UP", Message: "Answer", Time: fixed, IsUP: true, IsTrigger: true},
			},
		},
		"audit_logs": contentPage{Items: []state.AuditLog{{
			ID: 1, OccurredAt: fixed, RequestID: "request-1", Actor: "administrator", SessionID: "session-1",
			RemoteIP: "192.0.2.1", UserAgent: "contract-test", Action: "up.create", ResourceType: "up", ResourceID: "42",
			Outcome: state.AuditSuccess, HTTPMethod: http.MethodPost, Route: "/api/v1/ups", StatusCode: http.StatusCreated,
			DurationMS: 5, Details: map[string]any{"enabled": true},
		}}, Total: 1, Limit: 20},
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

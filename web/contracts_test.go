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

	response := fixture.request(t, http.MethodGet, "/api/v4/runtime", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var runtime runtimeView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &runtime))
	assert.NotEmpty(t, runtime.Timezone)
	assert.False(t, runtime.UpdatedAt.IsZero())

	response = fixture.request(t, http.MethodGet, "/api/v4/sources?platform=bilibili", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var sources []model.Source
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &sources))
	require.Len(t, sources, 1)
	assert.Equal(t, "42", sources[0].ExternalID)

	response = fixture.request(t, http.MethodGet, "/api/v4/channels", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	var channels []channelView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &channels))
	require.Len(t, channels, 1)
	assert.Equal(t, []string{"webhook"}, channels[0].ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), "https://example.com/webhook")

	response = fixture.request(t, http.MethodGet, "/api/v4/deliveries", nil, false)
	require.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"items":[],"page":{"next_cursor":"","has_more":false}}`, response.Body.String())
}

func TestWebSocketEventContract(t *testing.T) {
	t.Parallel()
	events := []wsEvent{
		{Event: "sync.required", Revision: 7, Topics: allResourceTopics()},
		{Event: "resources.invalidated", Revision: 8, Topics: []string{"runtime", "sources", "deliveries"}},
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
	content := model.Content{ID: "bilibili:content:dynamic-1", Platform: model.PlatformBilibili, SourceID: "bilibili:up:42", ExternalID: "dynamic-1",
		AuthorID: "42", AuthorName: "Contract UP", UpstreamType: "DYNAMIC_TYPE_AV", Type: model.ContentVideo, Title: "Contract video",
		Text: "Contract summary", URL: "https://t.bilibili.com/1", PublishedAt: fixed, FirstSeenAt: fixed.Add(time.Minute), LastSyncedAt: fixed.Add(2 * time.Minute),
		Stats: map[string]int64{"forwards": 1, "comments": 2, "likes": 3}}
	responses := map[string]any{
		"contents": cursorPageResponse{Items: []model.Content{content}, Page: cursorPage{}},
		"content_detail": contentDetailView{Content: content, Attachments: []attachmentView{{
			ID: content.ID + ":attachment:cover", ContentID: content.ID, ExternalID: "cover", Type: model.AttachmentImage,
			FileName: "cover.jpg", MIME: "image/jpeg", Size: 1024, Width: 1280, Height: 720, RemoteHost: "i0.hdslb.com", Localized: true,
		}}},
		"comment_tree": commentTreeView{Children: []commentNodeView{{
			ID: "bilibili:comment:root-1", Platform: model.PlatformBilibili, ContentID: content.ID, RootID: "bilibili:comment:root-1",
			AuthorID: "100", Role: model.RoleMember, Name: "Viewer", Message: "Question", PublishedAt: fixed.Add(-time.Minute),
			Children: []commentNodeView{{ID: "bilibili:comment:reply-1", Platform: model.PlatformBilibili, ContentID: content.ID,
				RootID: "bilibili:comment:root-1", ParentID: "bilibili:comment:root-1", AuthorID: "42", Role: model.RoleUP,
				Name: "Contract UP", Message: "Answer", PublishedAt: fixed, IsTrigger: true}},
		}}},
		"audit_logs": cursorPageResponse{Items: []state.AuditLog{{
			ID: 1, OccurredAt: fixed, RequestID: "request-1", Actor: "administrator", SessionID: "session-1",
			RemoteIP: "192.0.2.1", UserAgent: "contract-test", Action: "up.create", ResourceType: "up", ResourceID: "42",
			Outcome: state.AuditSuccess, HTTPMethod: http.MethodPost, Route: "/api/v4/ups", StatusCode: http.StatusCreated,
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

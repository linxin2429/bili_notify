package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorPaginationUsesStableTieBreakers(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	published := time.Date(2026, time.August, 9, 10, 11, 12, 0, time.UTC)
	source := model.Source{ID: "bilibili:up:42", Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "42", Name: "UP", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, fixture.store.PutSource(source))
	channel, err := fixture.store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": fixture.webhook.URL},
	})
	require.NoError(t, err)
	contents := []model.Content{
		{ID: "bilibili:content:dynamic-a", Platform: model.PlatformBilibili, SourceID: source.ID, ExternalID: "dynamic-a", AuthorID: "42", AuthorName: "UP", UpstreamType: "DYNAMIC_TYPE_WORD", Type: model.ContentDynamic, PublishedAt: published, FirstSeenAt: published, LastSyncedAt: published, Text: "a", URL: "https://t.bilibili.com/a"},
		{ID: "bilibili:content:dynamic-b", Platform: model.PlatformBilibili, SourceID: source.ID, ExternalID: "dynamic-b", AuthorID: "42", AuthorName: "UP", UpstreamType: "DYNAMIC_TYPE_WORD", Type: model.ContentDynamic, PublishedAt: published, FirstSeenAt: published, LastSyncedAt: published, Text: "b", URL: "https://t.bilibili.com/b"},
	}
	for _, content := range contents {
		require.NoError(t, fixture.store.ArchiveContentAndEnqueue(content, nil, []string{channel.ID}, true))
	}
	for index := range 2 {
		_, err := fixture.store.AppendAudit(state.AuditLog{
			OccurredAt: published, RequestID: fmt.Sprintf("request-%d", index), Action: "up.create",
			Outcome: state.AuditSuccess, HTTPMethod: http.MethodPost, Details: map[string]any{},
		})
		require.NoError(t, err)
	}

	tests := []struct {
		name       string
		path       string
		key        string
		wantFirst  string
		wantSecond string
	}{
		{name: "contents", path: "/api/v4/contents", key: "id", wantFirst: "bilibili:content:dynamic-b", wantSecond: "bilibili:content:dynamic-a"},
		{name: "audit logs", path: "/api/v4/audit-logs", key: "id", wantFirst: "2", wantSecond: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first := requestCursorTestPage(t, fixture, tt.path+"?limit=1", tt.key)
			assert.Equal(t, tt.wantFirst, first.itemKey)
			assert.True(t, first.hasMore)
			require.NotEmpty(t, first.nextCursor)

			second := requestCursorTestPage(t, fixture, tt.path+"?limit=1&after="+url.QueryEscape(first.nextCursor), tt.key)
			assert.Equal(t, tt.wantSecond, second.itemKey)
			assert.False(t, second.hasMore)
			assert.Empty(t, second.nextCursor)
		})
	}
}

func TestDynamicCursorDoesNotAdmitNewerInsertions(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	published := time.Date(2026, time.August, 9, 10, 11, 12, 0, time.UTC)
	source := model.Source{ID: "bilibili:up:42", Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "42", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, fixture.store.PutSource(source))
	archive := func(id string, at time.Time) {
		t.Helper()
		require.NoError(t, fixture.store.ArchiveContent(model.Content{ID: "bilibili:content:" + id, Platform: model.PlatformBilibili, SourceID: source.ID, ExternalID: id, UpstreamType: "DYNAMIC_TYPE_WORD", Type: model.ContentDynamic, PublishedAt: at, FirstSeenAt: at, LastSyncedAt: at}, nil))
	}
	archive("dynamic-a", published)
	archive("dynamic-b", published)
	first := requestCursorTestPage(t, fixture, "/api/v4/contents?limit=1", "id")
	require.Equal(t, "bilibili:content:dynamic-b", first.itemKey)

	archive("dynamic-c", published.Add(time.Minute))
	second := requestCursorTestPage(t, fixture, "/api/v4/contents?limit=1&after="+url.QueryEscape(first.nextCursor), "id")
	assert.Equal(t, "bilibili:content:dynamic-a", second.itemKey)
}

type cursorTestPage struct {
	itemKey    string
	nextCursor string
	hasMore    bool
}

func requestCursorTestPage(t *testing.T, fixture *adminAPIFixture, path, key string) cursorTestPage {
	t.Helper()
	response := fixture.request(t, http.MethodGet, path, nil, false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct {
		Items []map[string]any `json:"items"`
		Page  cursorPage       `json:"page"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	value := body.Items[0][key]
	if number, ok := value.(float64); ok {
		value = strconv.FormatInt(int64(number), 10)
	}
	itemKey, ok := value.(string)
	require.True(t, ok)
	return cursorTestPage{itemKey: itemKey, nextCursor: body.Page.NextCursor, hasMore: body.Page.HasMore}
}

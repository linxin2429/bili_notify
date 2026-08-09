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
	require.NoError(t, fixture.store.PutUP(model.UP{UID: "42", Name: "UP", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	channel, err := fixture.store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": fixture.webhook.URL},
	})
	require.NoError(t, err)
	dynamics := []model.Dynamic{
		{ID: "dynamic-a", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD", PublishedAt: published, Summary: "a", URL: "https://t.bilibili.com/a"},
		{ID: "dynamic-b", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD", PublishedAt: published, Summary: "b", URL: "https://t.bilibili.com/b"},
	}
	created, err := fixture.store.RecordDynamics("42", dynamics, []string{channel.ID}, state.DynamicBaselineNone)
	require.NoError(t, err)
	require.Equal(t, 2, created)
	target := model.CommentTarget{
		UID: "42", UPName: "UP", DynamicID: "dynamic-a", ContentType: "DYNAMIC_TYPE_WORD",
		URL: "https://t.bilibili.com/a", CommentType: 11, CommentOID: "oid", PublishedAt: published,
	}
	require.NoError(t, fixture.store.PutCommentTargets("42", []model.CommentTarget{target}))
	comments := []model.CommentNotification{
		{RPID: "reply-a", UPUID: "42", UPName: "UP", ContentType: "dynamic", ContentID: "dynamic-a", ContentURL: target.URL, PublishedAt: published, Thread: []model.CommentNode{}},
		{RPID: "reply-b", UPUID: "42", UPName: "UP", ContentType: "dynamic", ContentID: "dynamic-a", ContentURL: target.URL, PublishedAt: published, Thread: []model.CommentNode{}},
	}
	created, err = fixture.store.RecordCommentNotifications(target, comments, nil, false)
	require.NoError(t, err)
	require.Equal(t, 2, created)
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
		{name: "dynamics", path: "/api/v2/dynamics", key: "id", wantFirst: "dynamic-b", wantSecond: "dynamic-a"},
		{name: "comments", path: "/api/v2/comments", key: "rpid", wantFirst: "reply-b", wantSecond: "reply-a"},
		{name: "deliveries", path: "/api/v2/deliveries", key: "id", wantFirst: "dynamic-b:" + channel.ID, wantSecond: "dynamic-a:" + channel.ID},
		{name: "audit logs", path: "/api/v2/audit-logs", key: "id", wantFirst: "2", wantSecond: "1"},
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
	require.NoError(t, fixture.store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	_, err := fixture.store.RecordDynamics("42", []model.Dynamic{
		{ID: "dynamic-a", UID: "42", PublishedAt: published, Summary: "a", URL: "https://t.bilibili.com/a"},
		{ID: "dynamic-b", UID: "42", PublishedAt: published, Summary: "b", URL: "https://t.bilibili.com/b"},
	}, nil, state.DynamicBaselineNone)
	require.NoError(t, err)
	first := requestCursorTestPage(t, fixture, "/api/v2/dynamics?limit=1", "id")
	require.Equal(t, "dynamic-b", first.itemKey)

	_, err = fixture.store.RecordDynamics("42", []model.Dynamic{{
		ID: "dynamic-c", UID: "42", PublishedAt: published.Add(time.Minute), Summary: "c", URL: "https://t.bilibili.com/c",
	}}, nil, state.DynamicBaselineNone)
	require.NoError(t, err)
	second := requestCursorTestPage(t, fixture, "/api/v2/dynamics?limit=1&after="+url.QueryEscape(first.nextCursor), "id")
	assert.Equal(t, "dynamic-a", second.itemKey)
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

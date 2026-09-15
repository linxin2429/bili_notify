package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentListItem(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("字", contentListTextLimit+20)
	tests := []struct {
		name         string
		in           model.Content
		wantText     string
		wantSafeHTML string
	}{
		{name: "empty", in: model.Content{ID: "x"}, wantText: "", wantSafeHTML: ""},
		{name: "strips html keeps short text", in: model.Content{ID: "x", Text: "summary", SafeHTML: "<p>summary</p>"}, wantText: "summary"},
		{name: "exactly 280 runes", in: model.Content{Text: strings.Repeat("字", contentListTextLimit)}, wantText: strings.Repeat("字", contentListTextLimit)},
		{name: "truncates long CJK", in: model.Content{Text: long, SafeHTML: "<p>" + long + "</p>"}, wantText: strings.Repeat("字", contentListTextLimit)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := contentListItem(tt.in)
			assert.Equal(t, tt.wantText, got.Text)
			assert.Empty(t, got.SafeHTML)
			assert.LessOrEqual(t, utf8.RuneCountInString(got.Text), contentListTextLimit)
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "empty", value: "", limit: 10, want: ""},
		{name: "zero limit", value: "abc", limit: 0, want: ""},
		{name: "negative limit", value: "abc", limit: -1, want: ""},
		{name: "ascii short", value: "hi", limit: 3, want: "hi"},
		{name: "ascii exact", value: "hey", limit: 3, want: "hey"},
		{name: "ascii over", value: "hello", limit: 3, want: "hel"},
		{name: "cjk over", value: strings.Repeat("字", 281), limit: 280, want: strings.Repeat("字", 280)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, truncateRunes(tt.value, tt.limit))
		})
	}
}

func TestContentListOmitsHeavyFields(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, nil)
	published := time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC)
	source := model.Source{ID: "bilibili:up:42", Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP,
		ExternalID: "42", Name: "UP", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, fixture.store.PutSource(source))
	body := strings.Repeat("字", contentListTextLimit+40)
	content := model.Content{
		ID: model.ContentID(model.PlatformBilibili, "dynamic-long"), Platform: model.PlatformBilibili, SourceID: source.ID,
		ExternalID: "dynamic-long", AuthorID: "42", AuthorName: "UP", UpstreamType: "DYNAMIC_TYPE_WORD", Type: model.ContentDynamic,
		PublishedAt: published, FirstSeenAt: published, LastSyncedAt: published, Title: "title", Text: body,
		SafeHTML: "<p>" + body + "</p>", URL: "https://t.bilibili.com/1",
	}
	require.NoError(t, fixture.store.ArchiveContent(content, nil))

	list := fixture.request(t, http.MethodGet, "/api/v4/contents?source_id=bilibili:up:42&limit=1", nil, false)
	require.Equal(t, http.StatusOK, list.Code)
	var page struct {
		Items []model.Content `json:"items"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, strings.Repeat("字", contentListTextLimit), page.Items[0].Text)
	assert.Empty(t, page.Items[0].SafeHTML)
	assert.NotContains(t, list.Body.String(), "safe_html")
	assert.NotContains(t, list.Body.String(), "<p>")

	detail := fixture.request(t, http.MethodGet, "/api/v4/contents/"+content.ID, nil, false)
	require.Equal(t, http.StatusOK, detail.Code)
	var view contentDetailView
	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &view))
	assert.Equal(t, body, view.Content.Text)
	assert.Equal(t, "<p>"+body+"</p>", view.Content.SafeHTML)
}

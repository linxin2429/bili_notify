package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T, fill byte) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, fill))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestArchiveDynamicsInsertIgnoreAndSkipSystem(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 20)
	pub := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	items := []model.Dynamic{
		{ID: "d1", UID: "42", UPName: "Alice", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "Hello World", URL: "https://t.bilibili.com/d1"},
		{ID: "sys", UID: "system", UPName: "system", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "alert"},
	}
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	created, err := store.RecordDynamics("42", items, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
	items[0].Summary = "changed"
	_, err = store.RecordDynamics("42", items, nil, false)
	require.NoError(t, err)

	got, err := store.GetDynamic("d1")
	require.NoError(t, err)
	assert.Equal(t, "Hello World", got.Summary)

	_, err = store.GetDynamic("sys")
	assert.ErrorIs(t, err, ErrNotFound)

	list, total, err := store.QueryDynamics(ContentQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.True(t, list[0].Baseline)
	assert.Equal(t, "d1", list[0].ID)
}

func TestQueryDynamicsFilters(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dynamics := []model.Dynamic{
		{ID: "a1", UID: "1", UPName: "U1", Type: "DYNAMIC_TYPE_WORD", PublishedAt: base.Add(1 * time.Hour), Title: "alpha", Summary: "cat video"},
		{ID: "a2", UID: "1", UPName: "U1", Type: "DYNAMIC_TYPE_AV", PublishedAt: base.Add(2 * time.Hour), Title: "beta", Summary: "dog photo"},
		{ID: "b1", UID: "2", UPName: "U2", Type: "DYNAMIC_TYPE_WORD", PublishedAt: base.Add(3 * time.Hour), Title: "gamma", Summary: "cat meme"},
	}

	tests := []struct {
		name  string
		query ContentQuery
		ids   []string
		total int
	}{
		{name: "by uid", query: ContentQuery{UID: "1"}, ids: []string{"a2", "a1"}, total: 2},
		{name: "keyword case insensitive", query: ContentQuery{Q: "CAT"}, ids: []string{"b1", "a1"}, total: 2},
		{name: "time range half open", query: ContentQuery{From: base.Add(2 * time.Hour), To: base.Add(3 * time.Hour)}, ids: []string{"a2"}, total: 1},
		{name: "limit offset", query: ContentQuery{Limit: 1, Offset: 1}, ids: []string{"a2"}, total: 3},
		{name: "combined uid and keyword", query: ContentQuery{UID: "1", Q: "dog"}, ids: []string{"a2"}, total: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			local := openTestStore(t, 21)
			require.NoError(t, local.PutUP(model.UP{UID: "1", Enabled: true}))
			require.NoError(t, local.PutUP(model.UP{UID: "2", Enabled: true}))
			_, err := local.RecordDynamics("1", dynamics[:2], nil, false)
			require.NoError(t, err)
			_, err = local.RecordDynamics("2", dynamics[2:], nil, false)
			require.NoError(t, err)
			list, total, err := local.QueryDynamics(tt.query)
			require.NoError(t, err)
			assert.Equal(t, tt.total, total)
			got := make([]string, 0, len(list))
			for _, item := range list {
				got = append(got, item.ID)
			}
			assert.Equal(t, tt.ids, got)
		})
	}
}

func TestQueryDynamicsBuildsPreviewFromArchivedPayload(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		dynamic      model.Dynamic
		wantMedia    int
		wantOriginal bool
	}{
		{name: "plain text", dynamic: model.Dynamic{ID: "word", Type: "DYNAMIC_TYPE_WORD", Summary: "正文"}},
		{name: "single image", dynamic: model.Dynamic{ID: "single", Type: "DYNAMIC_TYPE_DRAW", Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.com/1.jpg"}}}, wantMedia: 1},
		{name: "multiple images", dynamic: model.Dynamic{ID: "multi", Type: "DYNAMIC_TYPE_DRAW", Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.com/1.jpg"}, {Kind: model.DynamicMediaImage, URL: "https://example.com/2.jpg"}}}, wantMedia: 2},
		{name: "video cover", dynamic: model.Dynamic{ID: "video", Type: "DYNAMIC_TYPE_AV", Media: []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://example.com/cover.jpg"}}}, wantMedia: 1},
		{name: "mixed text and image", dynamic: model.Dynamic{ID: "mixed", Type: "DYNAMIC_TYPE_DRAW", Description: "正文", Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.com/3.jpg"}}}, wantMedia: 1},
		{name: "forward preview", dynamic: model.Dynamic{ID: "forward", Type: "DYNAMIC_TYPE_FORWARD", Summary: "转发语", Original: &model.Dynamic{ID: "original", UPName: "原作者", Summary: "原文"}}, wantOriginal: true},
		{name: "no media", dynamic: model.Dynamic{ID: "empty", Type: "DYNAMIC_TYPE_COMMON_SQUARE"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 31)
			tt.dynamic.UID = "42"
			tt.dynamic.UPName = "UP"
			tt.dynamic.PublishedAt = base
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
			_, err := store.RecordDynamics("42", []model.Dynamic{tt.dynamic}, nil, false)
			require.NoError(t, err)

			items, total, err := store.QueryDynamics(ContentQuery{})
			require.NoError(t, err)
			assert.Equal(t, 1, total)
			require.Len(t, items, 1)
			assert.Len(t, items[0].Media, tt.wantMedia)
			if tt.wantOriginal {
				require.NotNil(t, items[0].Original)
				assert.Equal(t, "原文", items[0].Original.Summary)
			} else {
				assert.Nil(t, items[0].Original)
			}
		})
	}
}

func TestQueryDynamicsDegradesCorruptArchivedPayload(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 32)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	pub := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err := store.RecordDynamics("42", []model.Dynamic{
		{
			ID: "broken", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_DRAW", PublishedAt: pub, Summary: "坏档",
			Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.com/lost.jpg"}},
		},
		{
			ID: "ok", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_DRAW", PublishedAt: pub.Add(time.Hour), Summary: "好档",
			Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://example.com/ok.jpg"}},
		},
	}, nil, false)
	require.NoError(t, err)
	require.NoError(t, store.db.Model(&dynamicRow{}).Where("id = ?", "broken").Update("payload_json", "{").Error)

	items, total, err := store.QueryDynamics(ContentQuery{})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, items, 2)
	assert.Equal(t, "ok", items[0].ID)
	assert.Equal(t, "好档", items[0].Summary)
	require.Len(t, items[0].Media, 1)
	assert.Equal(t, "https://example.com/ok.jpg", items[0].Media[0].URL)
	assert.Equal(t, "broken", items[1].ID)
	assert.Equal(t, "坏档", items[1].Summary)
	assert.Empty(t, items[1].Media)
	assert.Nil(t, items[1].Original)
}

func TestArchiveAndQueryComments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 22)
	pub := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	target := model.CommentTarget{
		UID: "9", DynamicID: "d", ContentType: "DYNAMIC_TYPE_AV", URL: "https://b23.tv/a",
		CommentType: 1, CommentOID: "oid", PublishedAt: pub,
	}
	require.NoError(t, store.PutUP(model.UP{UID: "9", Enabled: true}))
	require.NoError(t, store.PutCommentTargets("9", []model.CommentTarget{target}))
	notes := []model.CommentNotification{
		{
			RPID: "r1", UPUID: "9", UPName: "Host", ContentTitle: "video A", ContentURL: "https://b23.tv/a",
			PublishedAt: pub, Thread: []model.CommentNode{
				{RPID: "root", Name: "fan", Message: "你好"},
				{RPID: "r1", Name: "Host", Message: "谢谢支持", IsUP: true, IsTrigger: true},
			},
		},
		{
			RPID: "r2", UPUID: "9", UPName: "Host", ContentTitle: "video B",
			PublishedAt: pub.Add(time.Hour), Thread: []model.CommentNode{
				{RPID: "r2", Name: "Host", Message: "update later", IsUP: true, IsTrigger: true},
			},
		},
	}
	_, err := store.RecordCommentNotifications(target, notes, nil, false)
	require.NoError(t, err)

	list, total, err := store.QueryComments(ContentQuery{Q: "谢谢"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, "r1", list[0].RPID)

	full, err := store.GetComment("r1")
	require.NoError(t, err)
	require.Len(t, full.Thread, 2)
	assert.Equal(t, "谢谢支持", full.Thread[1].Message)
}

func TestDeleteUPContent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 23)
	pub := time.Now()
	require.NoError(t, store.PutUP(model.UP{UID: "1", Enabled: true}))
	require.NoError(t, store.PutUP(model.UP{UID: "2", Enabled: true}))
	_, err := store.RecordDynamics("1", []model.Dynamic{
		{ID: "d1", UID: "1", UPName: "a", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "x"},
	}, nil, false)
	require.NoError(t, err)
	_, err = store.RecordDynamics("2", []model.Dynamic{
		{ID: "d2", UID: "2", UPName: "b", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "y"},
	}, nil, false)
	require.NoError(t, err)
	target := model.CommentTarget{UID: "1", CommentType: 1, CommentOID: "o", PublishedAt: pub}
	require.NoError(t, store.PutCommentTargets("1", []model.CommentTarget{target}))
	_, err = store.RecordCommentNotifications(target, []model.CommentNotification{
		{RPID: "c1", UPUID: "1", UPName: "a", PublishedAt: pub, Thread: []model.CommentNode{{Message: "m"}}},
	}, nil, false)
	require.NoError(t, err)
	require.NoError(t, store.DeleteUP("1"))

	_, total, err := store.QueryDynamics(ContentQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	_, err = store.GetDynamic("d1")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetComment("c1")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetDynamic("d2")
	require.NoError(t, err)
}

func TestRecordDynamicsArchivesAndDeleteUP(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 24)
	require.NoError(t, store.PutUP(model.UP{UID: "7", Name: "n", Enabled: true}))
	pub := time.Now().UTC().Truncate(time.Second)
	created, err := store.RecordDynamics("7", []model.Dynamic{
		{ID: "dyn7", UID: "7", UPName: "n", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "body"},
	}, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 0, created)

	list, total, err := store.QueryDynamics(ContentQuery{UID: "7"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.True(t, list[0].Baseline)

	require.NoError(t, store.DeleteUP("7"))
	_, total, err = store.QueryDynamics(ContentQuery{UID: "7"})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
}

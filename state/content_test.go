package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestContent(t *testing.T) *ContentStore {
	t.Helper()
	cs, err := OpenContent(filepath.Join(t.TempDir(), "content.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestArchiveDynamicsInsertIgnoreAndSkipSystem(t *testing.T) {
	t.Parallel()
	cs := openTestContent(t)
	pub := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	items := []model.Dynamic{
		{ID: "d1", UID: "42", UPName: "Alice", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "Hello World", URL: "https://t.bilibili.com/d1"},
		{ID: "sys", UID: "system", UPName: "system", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "alert"},
	}
	require.NoError(t, cs.ArchiveDynamics(items, true))
	// Second write with different summary must not overwrite.
	items[0].Summary = "changed"
	require.NoError(t, cs.ArchiveDynamics(items, false))

	got, err := cs.GetDynamic("d1")
	require.NoError(t, err)
	assert.Equal(t, "Hello World", got.Summary)

	_, err = cs.GetDynamic("sys")
	assert.ErrorIs(t, err, ErrNotFound)

	list, total, err := cs.QueryDynamics(ContentQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.True(t, list[0].Baseline)
	assert.Equal(t, "d1", list[0].ID)
}

func TestQueryDynamicsFilters(t *testing.T) {
	t.Parallel()
	cs := openTestContent(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dynamics := []model.Dynamic{
		{ID: "a1", UID: "1", UPName: "U1", Type: "DYNAMIC_TYPE_WORD", PublishedAt: base.Add(1 * time.Hour), Title: "alpha", Summary: "cat video"},
		{ID: "a2", UID: "1", UPName: "U1", Type: "DYNAMIC_TYPE_AV", PublishedAt: base.Add(2 * time.Hour), Title: "beta", Summary: "dog photo"},
		{ID: "b1", UID: "2", UPName: "U2", Type: "DYNAMIC_TYPE_WORD", PublishedAt: base.Add(3 * time.Hour), Title: "gamma", Summary: "cat meme"},
	}
	require.NoError(t, cs.ArchiveDynamics(dynamics, false))

	tests := []struct {
		name  string
		query ContentQuery
		ids   []string
		total int
	}{
		{
			name:  "by uid",
			query: ContentQuery{UID: "1"},
			ids:   []string{"a2", "a1"},
			total: 2,
		},
		{
			name:  "keyword case insensitive",
			query: ContentQuery{Q: "CAT"},
			ids:   []string{"b1", "a1"},
			total: 2,
		},
		{
			name:  "time range half open",
			query: ContentQuery{From: base.Add(2 * time.Hour), To: base.Add(3 * time.Hour)},
			ids:   []string{"a2"},
			total: 1,
		},
		{
			name:  "limit offset",
			query: ContentQuery{Limit: 1, Offset: 1},
			ids:   []string{"a2"},
			total: 3,
		},
		{
			name:  "combined uid and keyword",
			query: ContentQuery{UID: "1", Q: "dog"},
			ids:   []string{"a2"},
			total: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Query is read-only; share cs across subtests (same process, single conn).
			// Use a dedicated store per subtest to satisfy Parallel + single writer pool.
			local := openTestContent(t)
			require.NoError(t, local.ArchiveDynamics(dynamics, false))
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

func TestArchiveAndQueryComments(t *testing.T) {
	t.Parallel()
	cs := openTestContent(t)
	pub := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
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
	require.NoError(t, cs.ArchiveComments(notes, false))

	list, total, err := cs.QueryComments(ContentQuery{Q: "谢谢"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, "r1", list[0].RPID)

	full, err := cs.GetComment("r1")
	require.NoError(t, err)
	require.Len(t, full.Thread, 2)
	assert.Equal(t, "谢谢支持", full.Thread[1].Message)
}

func TestDeleteUPContent(t *testing.T) {
	t.Parallel()
	cs := openTestContent(t)
	pub := time.Now()
	require.NoError(t, cs.ArchiveDynamics([]model.Dynamic{
		{ID: "d1", UID: "1", UPName: "a", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "x"},
		{ID: "d2", UID: "2", UPName: "b", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "y"},
	}, false))
	require.NoError(t, cs.ArchiveComments([]model.CommentNotification{
		{RPID: "c1", UPUID: "1", UPName: "a", PublishedAt: pub, Thread: []model.CommentNode{{Message: "m"}}},
	}, false))
	require.NoError(t, cs.DeleteUPContent("1"))

	_, total, err := cs.QueryDynamics(ContentQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	_, err = cs.GetDynamic("d1")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = cs.GetComment("c1")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = cs.GetDynamic("d2")
	require.NoError(t, err)
}

func TestOpenWithContentHooksRecordAndDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v := mustVault(t, 21)
	store, err := OpenWithContent(filepath.Join(dir, "state.db"), filepath.Join(dir, "content.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.PutUP(model.UP{UID: "7", Name: "n", Enabled: true}))
	pub := time.Now().UTC().Truncate(time.Second)
	created, err := store.RecordDynamics("7", []model.Dynamic{
		{ID: "dyn7", UID: "7", UPName: "n", Type: "DYNAMIC_TYPE_WORD", PublishedAt: pub, Summary: "body"},
	}, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 0, created)

	list, total, err := store.Content().QueryDynamics(ContentQuery{UID: "7"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.True(t, list[0].Baseline)

	require.NoError(t, store.DeleteUP("7"))
	_, total, err = store.Content().QueryDynamics(ContentQuery{UID: "7"})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
}

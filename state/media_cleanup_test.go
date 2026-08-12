package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaCleanupTaskSurvivesRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	v := mustVault(t, 92)
	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)
	source := model.Source{ID: "zsxq:planet:1", Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "1", Name: "Planet", Enabled: true}
	require.NoError(t, store.PutSource(source))
	now := time.Now()
	content := model.Content{ID: "zsxq:content:1", Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "1", UpstreamType: "topic", Type: model.ContentDynamic, PublishedAt: now, FirstSeenAt: now, LastSyncedAt: now}
	attachment := model.Attachment{ID: content.ID + ":attachment:file", ContentID: content.ID, ExternalID: "file", Type: model.AttachmentFile, LocalPath: "media/zsxq/source/content/file.txt"}
	require.NoError(t, store.ArchiveContent(content, []model.Attachment{attachment}))
	require.NoError(t, store.DeleteSource(source.ID))
	require.NoError(t, store.Close())

	store, err = Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	task, err := store.NextMediaCleanupTask(t.Context(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, attachment.LocalPath, task.RelativePath)
	require.NoError(t, store.RetryMediaCleanupTask(t.Context(), task.ID, time.Now(), errors.New("busy"), false))
	task, err = store.NextMediaCleanupTask(t.Context(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, task.Attempts)
	require.NoError(t, store.CompleteMediaCleanupTask(t.Context(), task.ID))
	_, err = store.NextMediaCleanupTask(t.Context(), time.Now().Add(time.Minute))
	require.ErrorIs(t, err, media.ErrNoCleanupTask)
}

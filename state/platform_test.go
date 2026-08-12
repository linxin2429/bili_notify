package state

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgePlanetOutboxSnapshotsNonImageAttachments(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 199)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "planet"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "planet", Name: "Planet", Enabled: true}
	require.NoError(t, store.PutSource(source))
	putEnabledTestChannel(t, store)
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "topic"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic", UpstreamType: "talk", Type: model.ContentDiscussion, AuthorName: "Author", PublishedAt: time.Now(), URL: "https://example.test/topic"}
	attachments := []model.Attachment{
		{ID: "file", ContentID: content.ID, ExternalID: "f", Type: model.AttachmentFile, FileName: "original.pdf", MIME: "application/pdf", Size: 9, LocalPath: "media/zsxq/file-local.pdf"},
		{ID: "audio", ContentID: content.ID, ExternalID: "a", Type: model.AttachmentAudio, MIME: "audio/mpeg", LocalPath: "media/zsxq/local.mp3"},
		{ID: "video", ContentID: content.ID, ExternalID: "v", Type: model.AttachmentVideo, FileName: "clip.mp4", LocalizeError: "attachment localization failed"},
		{ID: "image", ContentID: content.ID, ExternalID: "i", Type: model.AttachmentImage, LocalPath: "media/zsxq/image.png"},
	}
	require.NoError(t, store.ArchiveContentAndEnqueue(content, attachments, nil, true))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Len(t, deliveries[0].Dynamic.Files, 3)
	assert.Equal(t, "original.pdf", deliveries[0].Dynamic.Files[0].Name)
	assert.Equal(t, "附件-2.mp3", deliveries[0].Dynamic.Files[1].Name)
	assert.Equal(t, "attachment localization failed", deliveries[0].Dynamic.Files[2].LocalizeError)

	attachments[0].FileName = "edited.pdf"
	require.NoError(t, store.ArchiveContent(content, attachments))
	after, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Equal(t, "original.pdf", after[0].Dynamic.Files[0].Name)
	assert.Equal(t, filepath.ToSlash("media/zsxq/file-local.pdf"), after[0].Dynamic.Files[0].LocalPath)
}

func TestCreateSourceEnforcesIdentityAndLimitAtomically(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*testing.T, *Store, model.Source)
		wantErr error
		wantMsg string
	}{
		{name: "creates source"},
		{name: "rejects duplicate", prepare: func(t *testing.T, store *Store, source model.Source) {
			t.Helper()
			require.NoError(t, store.CreateSource(source))
		}, wantErr: ErrSourceExists},
		{name: "rejects 101st Bilibili source", prepare: func(t *testing.T, store *Store, _ model.Source) {
			t.Helper()
			for index := range 100 {
				uid := strconv.Itoa(index + 1000)
				require.NoError(t, store.PutSource(model.Source{ID: model.SourceID(model.PlatformBilibili, uid), Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: uid, Name: uid, BaselineState: model.BaselinePending}))
			}
		}, wantMsg: "at most 100 UPs"},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, byte(160+index))
			source := model.Source{ID: model.SourceID(model.PlatformBilibili, "42"), Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "42", Name: "UP", BaselineState: model.BaselinePending}
			if tt.prepare != nil {
				tt.prepare(t, store, source)
			}
			err := store.CreateSource(source)
			if tt.wantErr != nil || tt.wantMsg != "" {
				require.Error(t, err)
				if tt.wantErr != nil {
					assert.True(t, errors.Is(err, tt.wantErr))
				}
				assert.Contains(t, err.Error(), tt.wantMsg)
				return
			}
			require.NoError(t, err)
			loaded, loadErr := store.Source(source.ID)
			require.NoError(t, loadErr)
			assert.Equal(t, source.Name, loaded.Name)
		})
	}
}

func TestPlatformIdentityAndEncryptedAccountSession(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 151)
	for _, source := range []model.Source{
		{ID: model.SourceID(model.PlatformBilibili, "42"), Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "42", Name: "UP"},
		{ID: model.SourceID(model.PlatformZSXQ, "42"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "42", Name: "Planet"},
	} {
		require.NoError(t, store.PutSource(source))
		content := model.Content{ID: model.ContentID(source.Platform, "same"), Platform: source.Platform, SourceID: source.ID, ExternalID: "same", UpstreamType: "test", Type: model.ContentDynamic, PublishedAt: time.Now()}
		require.NoError(t, store.ArchiveContent(content, nil))
	}
	items, err := store.QueryContents(PlatformContentQuery{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.NotEqual(t, items[0].ID, items[1].ID)

	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "7", DisplayName: "User", Status: model.AccountConnected, Session: map[string]string{"zsxq_access_token": "secret"}}
	require.NoError(t, store.PutPlatformAccount(account))
	loaded, err := store.PlatformAccount(model.PlatformZSXQ)
	require.NoError(t, err)
	assert.Equal(t, account.Session, loaded.Session)
	var persisted platformAccountRow
	require.NoError(t, store.db.Where("platform = ?", model.PlatformZSXQ).Take(&persisted).Error)
	assert.NotContains(t, string(persisted.SealedSession), "secret")
}

func TestSyncCommentTreeDigestDeletionEditAndRestore(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 152)
	putEnabledTestChannel(t, store)
	putEnabledTestChannel(t, store)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "9", Name: "Planet", OwnerID: "owner"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "topic"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: time.Now()}
	root := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "root"), RPID: "root", Role: model.RoleMember, AuthorID: "member", Time: time.Now()}
	owner := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "owner"), RPID: "owner", ParentID: root.ID, RootID: root.ID, Role: model.RoleOwner, AuthorID: "owner", Time: time.Now().Add(time.Second)}

	digests, err := store.SyncCommentTree(content, []model.CommentNode{root, owner}, true, true, "baseline", nil)
	require.NoError(t, err)
	assert.Empty(t, digests)

	newOwner := owner
	newOwner.ID, newOwner.RPID, newOwner.Message = model.CommentID(model.PlatformZSXQ, "owner-2"), "owner-2", "new"
	digests, err = store.SyncCommentTree(content, []model.CommentNode{root, owner, newOwner}, true, false, "batch", nil)
	require.NoError(t, err)
	require.Len(t, digests, 1)
	require.Len(t, digests[0].Triggers, 1)
	require.Len(t, digests[0].Paths, 1)
	assert.Equal(t, []string{root.ID, newOwner.ID}, []string{digests[0].Paths[0].Nodes[0].ID, digests[0].Paths[0].Nodes[1].ID})
	var outboxCount int64
	require.NoError(t, store.db.Model(&outboxRow{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(2), outboxCount)

	newOwner.Message = "edited"
	digests, err = store.SyncCommentTree(content, []model.CommentNode{root, owner, newOwner}, true, false, "edit", nil)
	require.NoError(t, err)
	assert.Empty(t, digests)
	require.NoError(t, store.SyncCommentTreeNoDigestForTest(content, []model.CommentNode{root, owner}, true))
	var deleted commentNodeRow
	require.NoError(t, store.db.Where("id = ?", newOwner.ID).Take(&deleted).Error)
	require.NotNil(t, deleted.DeletedAt)

	digests, err = store.SyncCommentTree(content, []model.CommentNode{root, owner, newOwner}, true, false, "restore", nil)
	require.NoError(t, err)
	assert.Empty(t, digests)
	require.NoError(t, store.db.Where("id = ?", newOwner.ID).Take(&deleted).Error)
	assert.Nil(t, deleted.DeletedAt)
}

func TestSyncCommentTreeMalformedSnapshotDoesNotDeleteMissingNodes(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 154)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "11"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "11", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "topic-malformed"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic-malformed", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: time.Now()}
	root := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "root"), RPID: "root", Role: model.RoleMember, Time: time.Now()}
	missingLater := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "existing"), RPID: "existing", Role: model.RoleMember, Time: time.Now().Add(time.Second)}
	_, err := store.SyncCommentTree(content, []model.CommentNode{root, missingLater}, true, true, "baseline", nil)
	require.NoError(t, err)

	orphan := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "orphan"), RPID: "orphan", ParentID: model.CommentID(model.PlatformZSXQ, "absent-parent"), RootID: root.ID, Role: model.RoleMember, Time: time.Now().Add(2 * time.Second)}
	_, err = store.SyncCommentTree(content, []model.CommentNode{root, orphan}, true, false, "malformed", nil)
	require.NoError(t, err)

	var persisted commentNodeRow
	require.NoError(t, store.db.Where("id = ?", missingLater.ID).Take(&persisted).Error)
	assert.Nil(t, persisted.DeletedAt)
	_, incomplete, err := store.CommentTree(content.ID)
	require.NoError(t, err)
	assert.True(t, incomplete)
}

func (s *Store) SyncCommentTreeNoDigestForTest(content model.Content, nodes []model.CommentNode, complete bool) error {
	_, err := s.SyncCommentTree(content, nodes, complete, false, "delete", nil)
	return err
}

func TestArchiveContentTransactionRollsBackInvalidAttachment(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 153)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "10"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "10", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "topic"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: time.Now()}
	err := store.ArchiveContentAndEnqueue(content, []model.Attachment{{ID: "bad", ContentID: "different", ExternalID: "asset", Type: model.AttachmentFile}}, []string{"channel"}, true)
	require.Error(t, err)
	_, _, err = store.Content(content.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

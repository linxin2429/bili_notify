package state

import (
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformAccountLifecycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 155)

	accounts, err := store.ListPlatformAccounts()
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	assert.Equal(t, model.AccountDisconnected, accounts[0].Status)
	assert.Equal(t, model.AccountDisconnected, accounts[1].Status)

	verifiedAt := time.Date(2026, time.August, 10, 1, 2, 3, 0, time.UTC)
	riskPausedUntil := verifiedAt.Add(time.Hour)
	account := model.PlatformAccount{
		Platform: model.PlatformBilibili, ExternalID: "42", DisplayName: "UP", Status: model.AccountConnected,
		Session: map[string]string{"SESSDATA": "secret"}, VerifiedAt: verifiedAt, UpdatedAt: verifiedAt,
		RiskPausedUntil: riskPausedUntil,
	}
	require.NoError(t, store.PutPlatformAccount(account))

	account.Session = nil
	account.DisplayName = "renamed"
	account.UpdatedAt = time.Time{}
	require.NoError(t, store.PutPlatformAccount(account))
	loaded, err := store.PlatformAccount(model.PlatformBilibili)
	require.NoError(t, err)
	assert.Equal(t, "renamed", loaded.DisplayName)
	assert.Equal(t, map[string]string{"SESSDATA": "secret"}, loaded.Session)
	assert.True(t, loaded.VerifiedAt.Equal(verifiedAt))
	assert.True(t, loaded.RiskPausedUntil.Equal(riskPausedUntil))
	assert.False(t, loaded.UpdatedAt.IsZero())

	require.NoError(t, store.SetPlatformAccountStatus(model.PlatformBilibili, model.AccountInvalid, "expired"))
	loaded, err = store.PlatformAccount(model.PlatformBilibili)
	require.NoError(t, err)
	assert.Equal(t, model.AccountInvalid, loaded.Status)
	assert.Equal(t, "expired", loaded.LastError)
	assert.Equal(t, map[string]string{"SESSDATA": "secret"}, loaded.Session)

	accounts, err = store.ListPlatformAccounts()
	require.NoError(t, err)
	require.Len(t, accounts, 2)
	assert.Nil(t, accounts[0].Session)
	assert.Equal(t, model.AccountInvalid, accounts[0].Status)

	require.NoError(t, store.DeletePlatformAccount(model.PlatformBilibili))
	_, err = store.PlatformAccount(model.PlatformBilibili)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, store.DeletePlatformAccount(model.PlatformBilibili), ErrNotFound)
}

func TestPlatformAccountRejectsInvalidPlatform(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 156)
	invalid := model.Platform("invalid")

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "put", run: func() error { return store.PutPlatformAccount(model.PlatformAccount{Platform: invalid}) }},
		{name: "get", run: func() error { _, err := store.PlatformAccount(invalid); return err }},
		{name: "delete", run: func() error { return store.DeletePlatformAccount(invalid) }},
		{name: "status", run: func() error { return store.SetPlatformAccountStatus(invalid, model.AccountInvalid, "bad") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Error(t, tt.run())
		})
	}
}

func TestPutZSXQPlatformAccountPreservesSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		externalID string
	}{
		{name: "first import", externalID: "account-1"},
		{name: "same account token refresh", externalID: "account-1"},
		{name: "different account", externalID: "account-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 158)
			require.NoError(t, store.PutSource(model.Source{ID: model.SourceID(model.PlatformZSXQ, "1"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
				ExternalID: "1", Name: "Disabled", Enabled: false, BaselineState: model.BaselineComplete}))
			require.NoError(t, store.PutSource(model.Source{ID: model.SourceID(model.PlatformZSXQ, "2"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
				ExternalID: "2", Name: "Enabled", Enabled: true, BaselineState: model.BaselineComplete}))
			if tt.name != "first import" {
				require.NoError(t, store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "account-1", DisplayName: "Old", Status: model.AccountConnected,
					Session: map[string]string{"zsxq_access_token": "old-token"}}))
			}
			account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: tt.externalID, DisplayName: "Member", Status: model.AccountConnected,
				Session: map[string]string{"zsxq_access_token": "new-token"}}
			require.NoError(t, store.PutPlatformAccount(account))
			for id, wantEnabled := range map[string]bool{"1": false, "2": true} {
				loaded, err := store.Source(model.SourceID(model.PlatformZSXQ, id))
				require.NoError(t, err)
				assert.Equal(t, wantEnabled, loaded.Enabled)
			}
		})
	}
}

func TestLegacyZSXQSessionWithoutAccessTokenIsInvalid(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 159)
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "account", DisplayName: "Member", Status: model.AccountConnected,
		Session: map[string]string{"legacy_cookie": "secret"}}
	require.NoError(t, store.PutPlatformAccount(account))
	loaded, err := store.PlatformAccount(model.PlatformZSXQ)
	require.NoError(t, err)
	assert.Equal(t, model.AccountInvalid, loaded.Status)
	assert.Equal(t, "session import required", loaded.LastError)
}

func TestEmptyZSXQSessionClearsSealedToken(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 160)
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "account", DisplayName: "Member", Status: model.AccountConnected,
		Session: map[string]string{"zsxq_access_token": "secret"}}
	require.NoError(t, store.PutPlatformAccount(account))
	account.Status = model.AccountInvalid
	account.Session = map[string]string{}
	require.NoError(t, store.PutPlatformAccount(account))
	loaded, err := store.PlatformAccount(model.PlatformZSXQ)
	require.NoError(t, err)
	assert.Empty(t, loaded.Session)
	assert.Equal(t, model.AccountInvalid, loaded.Status)
}

func TestListSourcesRejectsInvalidPlatform(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 157)
	_, err := store.ListSources(model.Platform("invalid"))
	assert.Error(t, err)
}

func TestCommentSyncStateLifecycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 158)
	contentID := model.ContentID(model.PlatformZSXQ, "topic")
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "sync"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "sync", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	require.NoError(t, store.ArchiveContent(model.Content{ID: contentID, Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: time.Now()}, nil))

	ready, err := store.CommentSyncState(model.PlatformZSXQ, contentID)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, store.PutCommentSyncState(model.PlatformZSXQ, contentID, false, time.Time{}, "first failure"))
	ready, err = store.CommentSyncState(model.PlatformZSXQ, contentID)
	require.NoError(t, err)
	assert.False(t, ready)

	syncedAt := time.Date(2026, time.August, 10, 3, 4, 5, 0, time.UTC)
	require.NoError(t, store.PutCommentSyncState(model.PlatformZSXQ, contentID, true, syncedAt, ""))
	ready, err = store.CommentSyncState(model.PlatformZSXQ, contentID)
	require.NoError(t, err)
	assert.True(t, ready)

	var row struct {
		LastSyncedAt *int64 `gorm:"column:last_synced_at"`
		LastError    string `gorm:"column:last_error"`
	}
	require.NoError(t, store.db.Table("sync_targets").Where("platform = ? AND content_id = ?", model.PlatformZSXQ, contentID).Take(&row).Error)
	require.NotNil(t, row.LastSyncedAt)
	assert.Equal(t, syncedAt.Unix(), *row.LastSyncedAt)
	assert.Empty(t, row.LastError)

	assert.Error(t, store.PutCommentSyncState(model.Platform("invalid"), contentID, true, syncedAt, ""))
}

func TestContentArchiveQueryAndDeletionLifecycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 159)
	published := time.Date(2026, time.August, 10, 4, 5, 6, 0, time.UTC)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "9", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{
		ID: model.ContentID(model.PlatformZSXQ, "topic"), Platform: model.PlatformZSXQ, SourceID: source.ID,
		ExternalID: "topic", AuthorName: "Author", UpstreamType: "talk", Type: model.ContentDiscussion,
		Title: "A Mixed CASE Title", Text: "searchable body", PublishedAt: published, Stats: map[string]int64{"comments": 3},
	}
	attachments := []model.Attachment{
		{ID: "asset-1", ContentID: content.ID, ExternalID: "1", Type: model.AttachmentImage, RemoteURL: "https://cdn.example.test/image.png", LocalPath: "media/image.png"},
		{ID: "asset-2", ContentID: content.ID, ExternalID: "2", Type: model.AttachmentFile, RemoteURL: "://bad-url", RemoteHost: "provided.example", FileName: "file.bin"},
	}
	require.NoError(t, store.ArchiveContent(content, attachments))

	tests := []struct {
		name  string
		query PlatformContentQuery
		count int
	}{
		{name: "platform source and folded keyword", query: PlatformContentQuery{Platform: model.PlatformZSXQ, SourceID: source.ID, Keyword: "mixed case", Limit: 10}, count: 1},
		{name: "from inclusive", query: PlatformContentQuery{From: published, Limit: 10}, count: 1},
		{name: "to exclusive", query: PlatformContentQuery{To: published, Limit: 10}, count: 0},
		{name: "cursor excludes item", query: PlatformContentQuery{AfterAt: published, AfterID: content.ID, Limit: 10}, count: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items, err := store.QueryContents(tt.query)
			require.NoError(t, err)
			assert.Len(t, items, tt.count)
		})
	}
	_, err := store.QueryContents(PlatformContentQuery{Platform: model.Platform("invalid")})
	assert.Error(t, err)

	loaded, loadedAttachments, err := store.Content(content.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), loaded.Stats["comments"])
	require.Len(t, loadedAttachments, 2)
	assert.Equal(t, "cdn.example.test", loadedAttachments[0].RemoteHost)
	assert.Empty(t, loadedAttachments[0].RemoteURL)
	remoteAttachment, err := store.Attachment(content.ID, "asset-1", true)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.test/image.png", remoteAttachment.RemoteURL)

	deletedAt := published.Add(time.Hour)
	require.NoError(t, store.MarkContentDeleted(content.ID, deletedAt))
	require.NoError(t, store.MarkContentDeleted(content.ID, deletedAt.Add(time.Hour)))
	loaded, _, err = store.Content(content.ID)
	require.NoError(t, err)
	assert.True(t, loaded.DeletedAt.Equal(deletedAt))
	assert.ErrorIs(t, store.MarkContentDeleted("missing", time.Time{}), ErrNotFound)

	content.Text = "restored"
	require.NoError(t, store.ArchiveContent(content, nil))
	loaded, _, err = store.Content(content.ID)
	require.NoError(t, err)
	assert.True(t, loaded.DeletedAt.IsZero())
	assert.Equal(t, "restored", loaded.Text)
}

func TestDeleteSourceRemovesPlatformState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 180)
	now := time.Date(2026, time.August, 10, 7, 8, 9, 0, time.UTC)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "delete"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "delete", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "delete"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "delete", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: now}
	channel, err := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.test/hook"}})
	require.NoError(t, err)
	require.NoError(t, store.ArchiveContentAndEnqueue(content, nil, nil, true))
	require.NoError(t, store.db.Model(&outboxRow{}).Where("channel_id = ?", channel.ID).Update("state", "blocked").Error)

	require.NoError(t, store.DeleteSource(source.ID))
	_, err = store.Source(source.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = store.Content(content.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	var deliveryCount, seenCount int64
	require.NoError(t, store.db.Model(&outboxRow{}).Count(&deliveryCount).Error)
	require.NoError(t, store.db.Model(&seenItemRow{}).Count(&seenCount).Error)
	assert.Zero(t, deliveryCount)
	assert.Zero(t, seenCount)
	assert.ErrorIs(t, store.DeleteSource(source.ID), ErrNotFound)
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int64
		want string
	}{
		{name: "bytes", size: 42, want: "42 B"},
		{name: "kibibytes", size: 1536, want: "1.5 KiB"},
		{name: "mebibytes", size: 2 * 1024 * 1024, want: "2.0 MiB"},
		{name: "gibibytes", size: 3 * 1024 * 1024 * 1024, want: "3.0 GiB"},
		{name: "tebibytes", size: 4 * 1024 * 1024 * 1024 * 1024, want: "4.0 TiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, humanBytes(tt.size))
		})
	}
}

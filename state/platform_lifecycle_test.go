package state

import (
	"encoding/json"
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

func TestMergeVisibleSourcesPreservesAdministratorState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 157)
	now := time.Date(2026, time.August, 10, 2, 3, 4, 0, time.UTC)
	existing := model.Source{
		ID: model.SourceID(model.PlatformZSXQ, "2"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
		ExternalID: "2", Name: "old", Note: "keep", OwnerID: "old-owner", OwnerName: "old owner",
		Enabled: true, BaselineState: model.BaselineRunning, BackfillCursor: "cursor", HighWatermark: "high",
		BackfillDone: 3, BackfillTotal: 9, LastSuccessAt: now,
	}
	require.NoError(t, store.PutSource(existing))

	visible := []model.Source{
		{ID: existing.ID, Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "2", Name: "new", OwnerID: "owner-2", OwnerName: "owner two"},
		{ID: model.SourceID(model.PlatformZSXQ, "1"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "1", Name: "first", OwnerID: "owner-1"},
	}
	require.NoError(t, store.MergeVisibleSources(model.PlatformZSXQ, visible))

	loaded, err := store.Source(existing.ID)
	require.NoError(t, err)
	assert.Equal(t, "new", loaded.Name)
	assert.Equal(t, "owner-2", loaded.OwnerID)
	assert.Equal(t, "keep", loaded.Note)
	assert.True(t, loaded.Enabled)
	assert.Equal(t, model.BaselineRunning, loaded.BaselineState)
	assert.Equal(t, "cursor", loaded.BackfillCursor)
	assert.Equal(t, int64(3), loaded.BackfillDone)
	assert.True(t, loaded.LastSuccessAt.Equal(now))

	items, err := store.ListSources(model.PlatformZSXQ)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "first", items[0].Name)
	assert.Equal(t, model.BaselinePending, items[0].BaselineState)

	tests := []struct {
		name     string
		platform model.Platform
		sources  []model.Source
	}{
		{name: "invalid platform", platform: model.Platform("invalid")},
		{name: "platform mismatch", platform: model.PlatformZSXQ, sources: []model.Source{{Platform: model.PlatformBilibili}}},
		{name: "invalid source", platform: model.PlatformZSXQ, sources: []model.Source{{Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Error(t, store.MergeVisibleSources(tt.platform, tt.sources))
		})
	}
	_, err = store.ListSources(model.Platform("invalid"))
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

func TestPromotePlatformOutbox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		seed   func(t *testing.T, store *Store, now time.Time)
		verify func(t *testing.T, delivery model.Delivery)
	}{
		{
			name: "content with image and file",
			seed: func(t *testing.T, store *Store, now time.Time) {
				source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "content"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "content", Name: "Planet"}
				require.NoError(t, store.PutSource(source))
				content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "topic"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "topic", AuthorName: "Author", UpstreamType: "talk", Type: model.ContentDiscussion, Title: "Topic", Text: "Body", URL: "https://example.test/topic", PublishedAt: now}
				attachments := []model.Attachment{
					{ID: "image", ContentID: content.ID, ExternalID: "image", Type: model.AttachmentImage, MIME: "image/png", Size: 1024, Width: 10, Height: 20, LocalPath: "media/image.png"},
					{ID: "file", ContentID: content.ID, ExternalID: "file", Type: model.AttachmentFile, Size: 2 * 1024 * 1024},
				}
				require.NoError(t, store.ArchiveContentAndEnqueue(content, attachments, []string{"channel"}, true))
			},
			verify: func(t *testing.T, delivery model.Delivery) {
				assert.Equal(t, model.DeliveryKindDynamic, delivery.Kind)
				assert.Equal(t, model.PlatformZSXQ, delivery.Dynamic.Platform)
				assert.Equal(t, "Planet", delivery.Dynamic.SourceName)
				require.Len(t, delivery.Dynamic.Media, 1)
				assert.Equal(t, "media/image.png", delivery.Dynamic.Media[0].LocalPath)
				require.Len(t, delivery.Dynamic.Links, 1)
				assert.Equal(t, "file (2.0 MiB)", delivery.Dynamic.Links[0].Text)
			},
		},
		{
			name: "owner comment digest",
			seed: func(t *testing.T, store *Store, now time.Time) {
				source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "comments"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "comments", Name: "Planet"}
				require.NoError(t, store.PutSource(source))
				content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "comments"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "comments", UpstreamType: "talk", Type: model.ContentDiscussion, Title: "Topic", URL: "https://example.test/comments", PublishedAt: now}
				root := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "root"), RPID: "root", Role: model.RoleMember, Time: now}
				_, err := store.SyncCommentTree(content, []model.CommentNode{root}, true, true, "baseline", nil)
				require.NoError(t, err)
				owner := model.CommentNode{ID: model.CommentID(model.PlatformZSXQ, "owner"), RPID: "owner-rpid", RootID: root.ID, ParentID: root.ID, Role: model.RoleOwner, Time: now.Add(time.Second)}
				_, err = store.SyncCommentTree(content, []model.CommentNode{root, owner}, true, false, "batch", []string{"channel"})
				require.NoError(t, err)
			},
			verify: func(t *testing.T, delivery model.Delivery) {
				assert.Equal(t, model.DeliveryKindComment, delivery.Kind)
				require.NotNil(t, delivery.Comment)
				assert.Equal(t, model.PlatformZSXQ, delivery.Comment.Platform)
				assert.Equal(t, "星球主", delivery.Comment.UPName)
				assert.Equal(t, "owner-rpid", delivery.Comment.RPID)
				require.Len(t, delivery.Comment.Thread, 2)
				assert.True(t, delivery.Comment.Thread[1].IsTrigger)
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, byte(160+index))
			now := time.Date(2026, time.August, 10, 5, 6, 7, 0, time.UTC)
			tt.seed(t, store, now)

			promoted, err := store.PromotePlatformOutbox(time.Now().Add(time.Minute), 0)
			require.NoError(t, err)
			assert.Equal(t, 1, promoted)
			deliveries, err := store.ListDeliveries(10)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			tt.verify(t, deliveries[0])

			promoted, err = store.PromotePlatformOutbox(time.Now().Add(time.Minute), 1)
			require.NoError(t, err)
			assert.Zero(t, promoted)
		})
	}
}

func TestPromotePlatformOutboxRejectsInvalidRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		row  outboxRow
	}{
		{name: "unsupported kind", row: outboxRow{Kind: "ai", PayloadJSON: `{}`}},
		{name: "invalid content", row: outboxRow{Kind: "content", PayloadJSON: `{`}},
		{name: "missing content source", row: outboxRow{Kind: "content", SourceID: "missing", PayloadJSON: `{"id":"zsxq:content:1","platform":"zsxq","source_id":"missing","external_id":"1","upstream_type":"talk","type":"discussion","published_at":"2026-08-10T00:00:00Z"}`}},
		{name: "invalid digest", row: outboxRow{Kind: "comment_digest", PayloadJSON: `{`}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, byte(170+index))
			now := time.Date(2026, time.August, 10, 6, 7, 8, 0, time.UTC)
			row := tt.row
			row.ID = "outbox"
			row.State = "pending"
			row.NextAt = now.Unix()
			row.CreatedAt = now.Unix()
			require.NoError(t, store.db.Create(&row).Error)

			promoted, err := store.PromotePlatformOutbox(now, 1)
			assert.Zero(t, promoted)
			assert.Error(t, err)
			var count int64
			require.NoError(t, store.db.Model(&outboxRow{}).Where("id = ?", row.ID).Count(&count).Error)
			assert.Equal(t, int64(1), count)
		})
	}
}

func TestDeleteSourceRemovesPlatformState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 180)
	now := time.Date(2026, time.August, 10, 7, 8, 9, 0, time.UTC)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "delete"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "delete", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "delete"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "delete", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: now}
	require.NoError(t, store.ArchiveContentAndEnqueue(content, nil, []string{"promoted", "pending"}, true))
	require.NoError(t, store.db.Model(&outboxRow{}).Where("channel_id = ?", "pending").Update("state", "blocked").Error)
	promoted, err := store.PromotePlatformOutbox(time.Now().Add(time.Minute), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, promoted)

	require.NoError(t, store.DeleteSource(source.ID))
	_, err = store.Source(source.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, _, err = store.Content(content.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	var outboxCount, deliveryCount, seenCount int64
	require.NoError(t, store.db.Model(&outboxRow{}).Count(&outboxCount).Error)
	require.NoError(t, store.db.Model(&deliveryRow{}).Count(&deliveryCount).Error)
	require.NoError(t, store.db.Model(&seenItemRow{}).Count(&seenCount).Error)
	assert.Zero(t, outboxCount)
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

func TestPlatformDeliveryPreservesProgressAndRejectsBadProgress(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 181)
	now := time.Date(2026, time.August, 10, 8, 9, 10, 0, time.UTC)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "progress"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "progress", Name: "Planet"}
	require.NoError(t, store.PutSource(source))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "progress"), Platform: model.PlatformZSXQ, SourceID: source.ID, ExternalID: "progress", UpstreamType: "talk", Type: model.ContentDiscussion, PublishedAt: now}
	payload, err := json.Marshal(content)
	require.NoError(t, err)
	rows := []outboxRow{
		{ID: "valid-progress", Kind: "content", SourceID: source.ID, ChannelID: "one", State: "", NextAt: now.Unix(), CreatedAt: now.Unix(), PayloadJSON: string(payload), ProgressJSON: `{"next_part":2}`},
		{ID: "bad-progress", Kind: "content", SourceID: source.ID, ChannelID: "two", State: "pending", NextAt: now.Unix(), CreatedAt: now.Unix(), PayloadJSON: string(payload), ProgressJSON: `{`},
	}
	for _, row := range rows {
		require.NoError(t, store.db.Create(&row).Error)
	}

	for _, tt := range []struct {
		name         string
		row          outboxRow
		wantProgress bool
	}{
		{name: "valid progress", row: rows[0], wantProgress: true},
		{name: "bad progress", row: rows[1], wantProgress: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delivery, err := platformDelivery(store.db, tt.row)
			require.NoError(t, err)
			assert.Equal(t, model.DeliveryPending, delivery.State)
			if tt.wantProgress {
				assert.NotNil(t, delivery.Progress)
			} else {
				assert.Nil(t, delivery.Progress)
			}
		})
	}
}

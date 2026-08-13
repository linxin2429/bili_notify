package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryDelivery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		missing    bool
		blocked    bool
		wantErr    error
		wantState  model.DeliveryState
		wantNextAt time.Time
	}{
		{name: "blocked delivery becomes immediately due", blocked: true, wantState: model.DeliveryPending, wantNextAt: time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)},
		{name: "pending delivery is rejected", wantErr: ErrDeliveryNotBlocked, wantState: model.DeliveryPending},
		{name: "missing delivery is rejected", missing: true, wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 91))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			id := "missing"
			var before model.Delivery
			if !tt.missing {
				require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
				channel, putErr := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
				require.NoError(t, putErr)
				_, recordErr := recordDynamicsForTest(store, "42", []model.Dynamic{{ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()}}, []string{channel.ID}, DynamicBaselineNone)
				require.NoError(t, recordErr)
				deliveries, listErr := store.ListDeliveries(0)
				require.NoError(t, listErr)
				require.Len(t, deliveries, 1)
				before = deliveries[0]
				id = before.ID
				if tt.blocked {
					progress := &model.DeliveryProgress{TextSent: true, ImagesSent: 2}
					require.NoError(t, store.FailDelivery(id, true, time.Now().Add(time.Hour), errors.New("permanent failure"), progress))
					deliveries, listErr = store.ListDeliveries(0)
					require.NoError(t, listErr)
					require.Len(t, deliveries, 1)
					before = deliveries[0]
				}
			}

			retryAt := tt.wantNextAt
			if retryAt.IsZero() {
				retryAt = time.Date(2026, time.August, 5, 16, 0, 0, 0, time.UTC)
			}
			err = store.RetryDelivery(id, retryAt)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.missing {
				return
			}

			deliveries, listErr := store.ListDeliveries(0)
			require.NoError(t, listErr)
			require.Len(t, deliveries, 1)
			got := deliveries[0]
			assert.Equal(t, tt.wantState, got.State)
			if tt.blocked {
				assert.True(t, retryAt.Equal(got.NextAt), "next_at: want %s, got %s", retryAt, got.NextAt)
				assert.Equal(t, before.Attempts, got.Attempts)
				assert.Equal(t, before.LastError, got.LastError)
				assert.Equal(t, before.Progress, got.Progress)
				assert.Equal(t, before.Content, got.Content)
			} else {
				assert.Equal(t, before.NextAt, got.NextAt)
			}
		})
	}
}

func TestBaselineAndDurableOutbox(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	v := mustVault(t, 1)
	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)

	up := model.UP{UID: "42", Enabled: true}
	require.NoError(t, store.PutUP(up))
	channel := model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}}
	channel, err = store.PutChannel(channel)
	require.NoError(t, err)

	first := model.Dynamic{ID: "1", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(), URL: "https://t.bilibili.com/1"}
	created, err := recordDynamicsForTest(store, "42", []model.Dynamic{first}, []string{channel.ID}, DynamicBaselineAll)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
	baselinedUP, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, baselinedUP.BaselineReady)
	assert.True(t, baselinedUP.ExclusiveBaselineReady)
	baselinedSource, err := store.Source(model.SourceID(model.PlatformBilibili, "42"))
	require.NoError(t, err)
	assert.Equal(t, model.BaselineComplete, baselinedSource.BaselineState)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)

	second := first
	second.ID = "2"
	second.Summary = "full body"
	second.Title = "video title"
	second.Media = []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://i0.hdslb.com/cover.jpg"}}
	second.Stats = &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3}
	second.Original = &model.Dynamic{ID: "original", UPName: "author", Type: "DYNAMIC_TYPE_WORD", Summary: "original body"}
	created, err = recordDynamicsForTest(store, "42", []model.Dynamic{first, second}, []string{channel.ID}, DynamicBaselineNone)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	require.NoError(t, store.Close())

	store, err = Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.ContentID(model.PlatformBilibili, "2"), deliveries[0].Content.ContentID)

	persisted := deliveries[0].Content
	require.NotNil(t, persisted)
	assert.Equal(t, "video title", persisted.Title)
	require.Len(t, persisted.Media, 1)
	require.NotNil(t, persisted.Stats)
	assert.Equal(t, int64(3), persisted.Stats["likes"])
	require.NotNil(t, persisted.ForwardOf)
	assert.Equal(t, "original body", persisted.ForwardOf.Text)
	require.NoError(t, store.CompleteDelivery(deliveries[0].ID))
}

func TestExclusiveDynamicBaselineKeepsNormalDeliveries(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 10))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true}))
	channel, err := store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)

	published := time.Now().UTC().Truncate(time.Second)
	exclusive := model.Dynamic{
		ID: "exclusive", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD",
		PublishedAt: published, Exclusive: true,
	}
	normal := model.Dynamic{
		ID: "normal", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD",
		PublishedAt: published.Add(time.Second),
	}
	created, err := recordDynamicsForTest(store,
		"42", []model.Dynamic{exclusive, normal}, []string{channel.ID}, DynamicBaselineExclusive,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, created)

	up, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)
	assert.True(t, up.ExclusiveBaselineReady)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "bilibili:content:normal", deliveries[0].Content.ContentID)

	records, err := store.QueryContents(PlatformContentQuery{SourceID: "bilibili:up:42"})
	require.NoError(t, err)
	require.Len(t, records, 2)
	byID := make(map[string]model.Content, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	assert.True(t, byID["bilibili:content:exclusive"].Baseline)
	assert.False(t, byID["bilibili:content:normal"].Baseline)
}

func TestMigrationRequiresExclusiveBaselineForExistingUPs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	legacyDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = legacyDB.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, legacyDB, migrations.FS)
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), 1)
	require.NoError(t, err)
	_, err = legacyDB.Exec(`INSERT INTO ups (uid, enabled, baseline_ready) VALUES ('42', 1, 1)`)
	require.NoError(t, err)
	require.NoError(t, legacyDB.Close())

	store, err := Open(t.Context(), path, mustVault(t, 11))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)
	assert.False(t, up.ExclusiveBaselineReady)
}

func TestAIProfileEnabledMigrationPreservesExistingProfiles(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "data.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), 5)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ai_profiles (id, kind, name, is_default, sealed, created_at, updated_at) VALUES ('profile', 'text', 'text', 1, X'00', 1, 1)`)
	require.NoError(t, err)
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	var enabled int
	require.NoError(t, db.QueryRow(`SELECT is_enabled FROM ai_profiles WHERE id = 'profile'`).Scan(&enabled))
	assert.Equal(t, 1, enabled)
}

func TestEncryptedRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	oldVault := mustVault(t, 2)
	store, err := Open(t.Context(), path, oldVault)
	require.NoError(t, err)
	_, err = store.PutChannel(model.Channel{Name: "mail", Type: model.ChannelEmail, Settings: map[string]string{
		"host": "smtp.example.com", "port": "465", "tls": "tls", "from": "a@example.com", "to": "b@example.com", "password": "secret",
	}})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	correct, err := Open(t.Context(), path, oldVault)
	require.NoError(t, err)
	t.Cleanup(func() { _ = correct.Close() })
	channels, err := correct.ListChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, "secret", channels[0].Settings["password"])
}

func TestMissingSession(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 4))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Session()
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAdministratorPasswordInitializationIsAtomic(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 9))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.AdminPasswordHash()
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.InitializeAdminPasswordHash("first"))
	require.ErrorIs(t, store.InitializeAdminPasswordHash("second"), ErrInitialized)
	require.NoError(t, store.SetAdminPasswordHash("changed"))
	hash, err := store.AdminPasswordHash()
	require.NoError(t, err)
	assert.Equal(t, "changed", hash)
}

func TestUpdateChannelSettingsMergesEncryptedRecord(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 5))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	channel, err := store.PutChannel(model.Channel{
		Name: "outlook", Type: model.ChannelMicrosoft,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555",
			"tenant":    "common", "to": "receiver@example.com",
		},
	})
	require.NoError(t, err)
	updated, err := store.UpdateChannelSettings(channel.ID, map[string]string{"refresh_token": "secret", "authorized": "true"})
	require.NoError(t, err)
	assert.Equal(t, "receiver@example.com", updated.Settings["to"])
	assert.Equal(t, "secret", updated.Settings["refresh_token"])
	loaded, err := store.Channel(channel.ID)
	require.NoError(t, err)
	assert.Equal(t, "secret", loaded.Settings["refresh_token"])
}

func TestRuntimeSettingsMissingAndRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 6))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.RuntimeSettings()
	require.ErrorIs(t, err, ErrNotFound)

	tests := []struct {
		name     string
		settings model.RuntimeSettings
		wantErr  string
	}{
		{
			name: "valid",
			settings: func() model.RuntimeSettings {
				settings := model.DefaultRuntimeSettings()
				settings.BilibiliDynamicIntervalSec, settings.BilibiliRequestRate, settings.BilibiliRequestConcurrency = 45, 1.5, 3
				return settings
			}(),
		},
		{
			name: "reject short poll",
			settings: func() model.RuntimeSettings {
				settings := model.DefaultRuntimeSettings()
				settings.BilibiliDynamicIntervalSec = 5
				return settings
			}(),
			wantErr: "poll interval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Parallel subtests share store; use a dedicated store for write isolation.
			local, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 7))
			require.NoError(t, err)
			t.Cleanup(func() { _ = local.Close() })

			err = local.PutRuntimeSettings(tt.settings)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				_, getErr := local.RuntimeSettings()
				require.ErrorIs(t, getErr, ErrNotFound)
				return
			}
			require.NoError(t, err)
			loaded, err := local.RuntimeSettings()
			require.NoError(t, err)
			assert.Equal(t, tt.settings, loaded)
		})
	}
}

func TestRuntimeSettingsRejectsVersionMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stored string
	}{
		{name: "unversioned", stored: `{"poll_interval_sec":45}`},
		{name: "previous version", stored: `{"_version":1}`},
		{name: "future version", stored: `{"_version":99}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 8))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.db.Create(&metaRow{Key: metaKeyRuntimeSettings, Value: tt.stored}).Error)

			_, err = store.RuntimeSettings()
			require.ErrorIs(t, err, ErrRuntimeSettingsVersionMismatch)
			assert.Contains(t, err.Error(), "fresh data volume")
		})
	}
}

func TestRuntimeSettingsVersionTwoDefaultsMissingAutomaticAIFlagToDisabled(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 9))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	raw, err := json.Marshal(runtimeSettingsRecord{Version: 2, RuntimeSettings: model.DefaultRuntimeSettings()})
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(raw, &stored))
	delete(stored, "ai_auto_processing_enabled")
	raw, err = json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, store.db.Create(&metaRow{Key: metaKeyRuntimeSettings, Value: string(raw)}).Error)

	settings, err := store.RuntimeSettings()
	require.NoError(t, err)
	assert.False(t, settings.AIAutoProcessingEnabled)
}

func TestRuntimeSettingsRejectsInvalidRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		stored  string
		wantErr string
	}{
		{name: "malformed JSON", stored: `{`, wantErr: "decoding runtime settings"},
		{name: "invalid current record", stored: `{"_version":2}`, wantErr: "validating stored runtime settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 10))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.db.Create(&metaRow{Key: metaKeyRuntimeSettings, Value: tt.stored}).Error)
			_, err = store.RuntimeSettings()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func mustVault(t *testing.T, fill byte) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{fill}, 32))
	require.NoError(t, err)
	return v
}

func TestCommentTargetsAndOutbox(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 8))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	_, err = store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)

	now := time.Now()
	discovered := []model.CommentTarget{
		{UID: "42", DynamicID: "d1", ContentType: "DYNAMIC_TYPE_AV", Title: "v1", URL: "https://t.bilibili.com/d1", CommentType: 1, CommentOID: "100", PublishedAt: now.Add(-time.Hour)},
		{UID: "42", DynamicID: "d2", ContentType: "DYNAMIC_TYPE_WORD", Title: "w2", URL: "https://t.bilibili.com/d2", CommentType: 17, CommentOID: "200", PublishedAt: now},
		{UID: "42", DynamicID: "d0", ContentType: "DYNAMIC_TYPE_WORD", URL: "https://t.bilibili.com/d0", CommentType: 17, CommentOID: "50", PublishedAt: now.Add(-2 * time.Hour)},
	}
	for _, target := range discovered {
		_, recordErr := recordDynamicsForTest(store, "42", []model.Dynamic{{
			ID: target.DynamicID, UID: "42", UPName: "up", Type: target.ContentType,
			PublishedAt: target.PublishedAt, URL: target.URL, Title: target.Title,
		}}, nil, DynamicBaselineAll)
		require.NoError(t, recordErr)
	}
	kept, err := store.UpsertCommentTargets("42", discovered, 2)
	require.NoError(t, err)
	require.Len(t, kept, 2)
	assert.Equal(t, "200", kept[0].CommentOID)
	assert.Equal(t, "100", kept[1].CommentOID)

	// Preserve baseline flag across upsert.
	kept[0].BaselineReady = true
	require.NoError(t, store.PutCommentTargets("42", kept))
	again, err := store.UpsertCommentTargets("42", []model.CommentTarget{{
		UID: "42", DynamicID: "d2", ContentType: "DYNAMIC_TYPE_WORD", URL: "https://t.bilibili.com/d2",
		CommentType: 17, CommentOID: "200", PublishedAt: now, CommentCount: 9,
	}}, 2)
	require.NoError(t, err)
	require.True(t, again[0].BaselineReady)
	assert.Equal(t, int64(9), again[0].CommentCount)

	target := again[0]
	content := model.Content{
		ID: model.ContentID(model.PlatformBilibili, target.DynamicID), Platform: model.PlatformBilibili,
		SourceID: model.SourceID(model.PlatformBilibili, target.UID), ExternalID: target.DynamicID,
		UpstreamType: target.ContentType, Type: model.ContentDynamic, Title: target.Title,
		URL: target.URL, PublishedAt: target.PublishedAt,
	}
	root := model.CommentNode{RPID: "r0", Name: "fan", Role: model.RoleMember, Message: "hi", Time: now}
	reply := model.CommentNode{RPID: "r1", Parent: "r0", Name: "up", Role: model.RoleUP, Message: "reply", Time: now}
	digests, err := store.SyncCommentTree(content, []model.CommentNode{root, reply}, true, true, "baseline", &target)
	require.NoError(t, err)
	assert.Empty(t, digests)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	seen, err := store.CommentSeen("42", "r1")
	require.NoError(t, err)
	assert.True(t, seen)

	reply2 := model.CommentNode{RPID: "r2", Parent: "r0", Name: "up", Role: model.RoleUP, Message: "again", Time: now.Add(time.Second)}
	digests, err = store.SyncCommentTree(content, []model.CommentNode{root, reply, reply2}, true, false, "incremental", &target)
	require.NoError(t, err)
	require.Len(t, digests, 1)
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.DeliveryKindComment, deliveries[0].Kind)
	require.NotNil(t, deliveries[0].Comment)
	assert.Equal(t, "r2", deliveries[0].Comment.RPID)

	require.NoError(t, store.DeleteUP("42"))
	targets, err := store.ListCommentTargets("42")
	require.NoError(t, err)
	assert.Empty(t, targets)
	seen, err = store.CommentSeen("42", "r2")
	require.NoError(t, err)
	assert.False(t, seen)
}

func TestSessionSwitchResetsAccountScopedCollectionState(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 9))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	first := model.BiliSession{AccountUID: "100", AccountName: "first", Cookies: map[string]string{"SESSDATA": "a"}, UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(first))
	account, err := store.PlatformAccount(model.PlatformBilibili)
	require.NoError(t, err)
	assert.Equal(t, model.AccountConnected, account.Status)
	assert.Equal(t, first.AccountUID, account.ExternalID)
	assert.Equal(t, first.AccountName, account.DisplayName)
	assert.Equal(t, first.Cookies, account.Session)
	require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": model.Followed}, time.Now()))
	require.NoError(t, store.MarkSpaceSynced("100", "42", time.Now()))
	require.NoError(t, store.InitializeFeed("100", "baseline-a", time.Now()))

	first.AccountName = "renamed"
	require.NoError(t, store.SaveSession(first))
	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.Equal(t, "baseline-a", feed.UpdateBaseline)
	relations, err := store.FollowRelations("100")
	require.NoError(t, err)
	assert.True(t, relations["42"].SpaceSynced)

	second := model.BiliSession{AccountUID: "200", AccountName: "second", Cookies: map[string]string{"SESSDATA": "b"}, UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(second))
	_, err = store.FeedState("100")
	require.ErrorIs(t, err, ErrNotFound)
	feed, err = store.FeedState("200")
	require.NoError(t, err)
	assert.False(t, feed.Initialized)
	relations, err = store.FollowRelations("200")
	require.NoError(t, err)
	assert.Empty(t, relations)
}

func TestUPCollectionRouteUsesFollowAndSynchronizationState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		followState model.FollowState
		synced      bool
		initialized bool
		wantRoute   model.CollectionRoute
	}{
		{name: "followed and synchronized", followState: model.Followed, synced: true, initialized: true, wantRoute: model.CollectionRouteFeedAll},
		{name: "followed but not synchronized", followState: model.Followed, initialized: true, wantRoute: model.CollectionRouteSpace},
		{name: "unfollowed", followState: model.FollowUnfollowed, synced: true, initialized: true, wantRoute: model.CollectionRouteSpace},
		{name: "feed not initialized", followState: model.Followed, synced: true, wantRoute: model.CollectionRouteSpace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 10))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "100", Cookies: map[string]string{"SESSDATA": "a"}}))
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
			require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": tt.followState}, time.Now()))
			if tt.synced {
				require.NoError(t, store.MarkSpaceSynced("100", "42", time.Now()))
			}
			if tt.initialized {
				require.NoError(t, store.InitializeFeed("100", "baseline", time.Now()))
			}
			up, err := store.UP("42")
			require.NoError(t, err)
			assert.Equal(t, tt.followState, up.FollowState)
			assert.Equal(t, tt.wantRoute, up.CollectionRoute)
		})
	}
}

func TestRecordFeedDynamicsAdvancesCursorWithDurableOutbox(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 11))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "100", Cookies: map[string]string{"SESSDATA": "a"}}))
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	require.NoError(t, store.PutFollowRelations("100", map[string]model.FollowState{"42": model.Followed}, time.Now()))
	require.NoError(t, store.MarkSpaceSynced("100", "42", time.Now()))
	require.NoError(t, store.InitializeFeed("100", "old", time.Now()))
	putEnabledTestChannel(t, store)

	dynamic := model.Dynamic{ID: "dynamic-1", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(), Summary: "new"}
	created, err := recordFeedDynamicsForTest(store, "100", "new", []model.Dynamic{dynamic}, []string{"channel"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	feed, err := store.FeedState("100")
	require.NoError(t, err)
	assert.Equal(t, "new", feed.UpdateBaseline)
	seen, err := store.Seen("42", "dynamic-1")
	require.NoError(t, err)
	assert.True(t, seen)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.ContentID(model.PlatformBilibili, "dynamic-1"), deliveries[0].Content.ContentID)
}

func TestChannelEnablementSuspendsAndResumesOutbox(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 17))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	channel := putEnabledTestChannel(t, store)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	_, err = recordDynamicsForTest(store, "42", []model.Dynamic{{ID: "suspend", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()}}, nil, DynamicBaselineNone)
	require.NoError(t, err)

	channel.Enabled = false
	_, err = store.PutChannel(channel)
	require.NoError(t, err)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.DeliverySuspended, deliveries[0].State)
	assert.Empty(t, mustDueDeliveries(t, store))

	channel.Enabled = true
	_, err = store.PutChannel(channel)
	require.NoError(t, err)
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.DeliveryPending, deliveries[0].State)
	assert.Len(t, mustDueDeliveries(t, store), 1)
}

func mustDueDeliveries(t *testing.T, store *Store) []model.Delivery {
	t.Helper()
	deliveries, err := store.DueDeliveries(time.Now().Add(time.Second), 10)
	require.NoError(t, err)
	return deliveries
}

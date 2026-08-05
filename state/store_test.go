package state

import (
	"bytes"
	"database/sql"
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
			store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 91))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			id := "missing"
			var before model.Delivery
			if !tt.missing {
				require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
				channel, putErr := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
				require.NoError(t, putErr)
				_, recordErr := store.RecordDynamics("42", []model.Dynamic{{ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()}}, []string{channel.ID}, DynamicBaselineNone)
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
				assert.Equal(t, before.Dynamic, got.Dynamic)
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
	store, err := Open(path, v)
	require.NoError(t, err)

	up := model.UP{UID: "42", Enabled: true}
	require.NoError(t, store.PutUP(up))
	channel := model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}}
	channel, err = store.PutChannel(channel)
	require.NoError(t, err)

	first := model.Dynamic{ID: "1", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(), URL: "https://t.bilibili.com/1"}
	created, err := store.RecordDynamics("42", []model.Dynamic{first}, []string{channel.ID}, DynamicBaselineAll)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
	baselinedUP, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, baselinedUP.BaselineReady)
	assert.True(t, baselinedUP.ExclusiveBaselineReady)
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
	created, err = store.RecordDynamics("42", []model.Dynamic{first, second}, []string{channel.ID}, DynamicBaselineNone)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	require.NoError(t, store.Close())

	store, err = Open(path, v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "2", deliveries[0].Dynamic.ID)

	persisted := deliveries[0].Dynamic
	assert.Equal(t, "video title", persisted.Title)
	require.Len(t, persisted.Media, 1)
	require.NotNil(t, persisted.Stats)
	assert.Equal(t, int64(3), persisted.Stats.Likes)
	require.NotNil(t, persisted.Original)
	assert.Equal(t, "original body", persisted.Original.Summary)
	require.NoError(t, store.CompleteDelivery(deliveries[0].ID))
}

func TestExclusiveDynamicBaselineKeepsNormalDeliveries(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 10))
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
	created, err := store.RecordDynamics(
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
	assert.Equal(t, "normal", deliveries[0].Dynamic.ID)

	records, total, err := store.QueryDynamics(ContentQuery{UID: "42"})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, records, 2)
	byID := make(map[string]DynamicRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	assert.True(t, byID["exclusive"].Baseline)
	assert.False(t, byID["normal"].Baseline)
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

	store, err := Open(path, mustVault(t, 11))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	up, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)
	assert.False(t, up.ExclusiveBaselineReady)
}

func TestEncryptedRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	oldVault := mustVault(t, 2)
	store, err := Open(path, oldVault)
	require.NoError(t, err)
	_, err = store.PutChannel(model.Channel{Name: "mail", Type: model.ChannelEmail, Settings: map[string]string{
		"host": "smtp.example.com", "port": "465", "tls": "tls", "from": "a@example.com", "to": "b@example.com", "password": "secret",
	}})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	correct, err := Open(path, oldVault)
	require.NoError(t, err)
	t.Cleanup(func() { _ = correct.Close() })
	channels, err := correct.ListChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, "secret", channels[0].Settings["password"])
}

func TestMissingSession(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 4))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Session()
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAdministratorPasswordInitializationIsAtomic(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 9))
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
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 5))
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
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 6))
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
			settings: model.RuntimeSettings{
				PollIntervalSec: 45, RequestRate: 1.5, RequestConcurrency: 3,
			},
		},
		{
			name: "reject short poll",
			settings: model.RuntimeSettings{
				PollIntervalSec: 5, RequestRate: 2, RequestConcurrency: 4,
			},
			wantErr: "poll interval",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Parallel subtests share store; use a dedicated store for write isolation.
			local, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 7))
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
			assert.Equal(t, tt.settings.WithCommentDefaults(), loaded)
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
	store, err := Open(filepath.Join(t.TempDir(), "data.db"), mustVault(t, 8))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	channel, err := store.PutChannel(model.Channel{
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
	note := model.CommentNotification{
		RPID: "r1", UPUID: "42", UPName: "up", ContentType: "DYNAMIC_TYPE_WORD",
		ContentID: "d2", ContentURL: target.URL, PublishedAt: now,
		Thread: []model.CommentNode{{RPID: "r0", Name: "fan", Message: "hi"}, {RPID: "r1", Name: "up", Message: "reply", IsUP: true, IsTrigger: true}},
	}
	created, err := store.RecordCommentNotifications(target, []model.CommentNotification{note}, []string{channel.ID}, true)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	seen, err := store.CommentSeen("42", "r1")
	require.NoError(t, err)
	assert.True(t, seen)

	note2 := note
	note2.RPID = "r2"
	note2.Thread = append(note2.Thread, model.CommentNode{RPID: "r2", Name: "up", Message: "again", IsUP: true, IsTrigger: true})
	created, err = store.RecordCommentNotifications(target, []model.CommentNotification{note, note2}, []string{channel.ID}, false)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, model.DeliveryKindComment, deliveries[0].EffectiveKind())
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

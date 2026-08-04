package state

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaselineAndDurableOutbox(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	v := mustVault(t, 1)
	store, err := Open(path, v)
	require.NoError(t, err)

	up := model.UP{UID: "42", Enabled: true}
	require.NoError(t, store.PutUP(up))
	channel := model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}}
	channel, err = store.PutChannel(channel)
	require.NoError(t, err)

	first := model.Dynamic{ID: "1", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now().UTC(), URL: "https://t.bilibili.com/1"}
	created, err := store.RecordDynamics("42", []model.Dynamic{first}, []string{channel.ID}, true)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
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
	created, err = store.RecordDynamics("42", []model.Dynamic{first, second}, []string{channel.ID}, false)
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

func TestEncryptedRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
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
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 4))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Session()
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAdministratorPasswordInitializationIsAtomic(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 9))
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
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 5))
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
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 6))
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
			local, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 7))
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

func mustVault(t *testing.T, fill byte) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{fill}, 32))
	require.NoError(t, err)
	return v
}

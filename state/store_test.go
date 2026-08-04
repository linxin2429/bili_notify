package state

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaselineAndDurableOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	v := mustVault(t, 1)
	store, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	up := model.UP{UID: "42", Enabled: true}
	if err := store.PutUP(up); err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}}
	channel, err = store.PutChannel(channel)
	if err != nil {
		t.Fatal(err)
	}
	first := model.Dynamic{ID: "1", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now().UTC(), URL: "https://t.bilibili.com/1"}
	created, err := store.RecordDynamics("42", []model.Dynamic{first}, []string{channel.ID}, true)
	if err != nil || created != 0 {
		t.Fatalf("baseline created=%d err=%v", created, err)
	}
	if deliveries, _ := store.ListDeliveries(0); len(deliveries) != 0 {
		t.Fatalf("baseline created %d deliveries", len(deliveries))
	}
	second := first
	second.ID = "2"
	second.Summary = "full body"
	second.Title = "video title"
	second.Media = []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://i0.hdslb.com/cover.jpg"}}
	second.Stats = &model.DynamicStats{Forwards: 1, Comments: 2, Likes: 3}
	second.Original = &model.Dynamic{ID: "original", UPName: "author", Type: "DYNAMIC_TYPE_WORD", Summary: "original body"}
	created, err = store.RecordDynamics("42", []model.Dynamic{first, second}, []string{channel.ID}, false)
	if err != nil || created != 1 {
		t.Fatalf("new dynamics created=%d err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deliveries, err := store.ListDeliveries(0)
	if err != nil || len(deliveries) != 1 || deliveries[0].Dynamic.ID != "2" {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
	}
	persisted := deliveries[0].Dynamic
	if persisted.Title != "video title" || len(persisted.Media) != 1 || persisted.Stats == nil || persisted.Stats.Likes != 3 || persisted.Original == nil || persisted.Original.Summary != "original body" {
		t.Fatalf("rich dynamic was not preserved: %#v", persisted)
	}
	if err := store.CompleteDelivery(deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	oldVault := mustVault(t, 2)
	store, err := Open(path, oldVault)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PutChannel(model.Channel{Name: "mail", Type: model.ChannelEmail, Settings: map[string]string{
		"host": "smtp.example.com", "port": "465", "tls": "tls", "from": "a@example.com", "to": "b@example.com", "password": "secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	correct, err := Open(path, oldVault)
	if err != nil {
		t.Fatal(err)
	}
	defer correct.Close()
	channels, err := correct.ListChannels()
	if err != nil || channels[0].Settings["password"] != "secret" {
		t.Fatalf("channels=%#v err=%v", channels, err)
	}
}

func TestMissingSession(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Session(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Session() error=%v, want ErrNotFound", err)
	}
}

func TestAdministratorPasswordInitializationIsAtomic(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AdminPasswordHash(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AdminPasswordHash() error=%v, want ErrNotFound", err)
	}
	if err := store.InitializeAdminPasswordHash("first"); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeAdminPasswordHash("second"); !errors.Is(err, ErrInitialized) {
		t.Fatalf("second initialization error=%v, want ErrInitialized", err)
	}
	if err := store.SetAdminPasswordHash("changed"); err != nil {
		t.Fatal(err)
	}
	if hash, err := store.AdminPasswordHash(); err != nil || hash != "changed" {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
}

func TestUpdateChannelSettingsMergesEncryptedRecord(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), mustVault(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel, err := store.PutChannel(model.Channel{
		Name: "outlook", Type: model.ChannelMicrosoft,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555",
			"tenant":    "common", "to": "receiver@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateChannelSettings(channel.ID, map[string]string{"refresh_token": "secret", "authorized": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Settings["to"] != "receiver@example.com" || updated.Settings["refresh_token"] != "secret" {
		t.Fatalf("settings = %#v", updated.Settings)
	}
	loaded, err := store.Channel(channel.ID)
	if err != nil || loaded.Settings["refresh_token"] != "secret" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	return v
}

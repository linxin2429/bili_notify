package state

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBiliSessionPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		legacy  bool
		pending string
	}{{name: "legacy", legacy: true}, {name: "renewable"}, {name: "pending confirmation", pending: "previous-secret"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "data.db")
			v := mustVault(t, 31)
			store, err := Open(t.Context(), path, v)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			original := model.BiliSession{Cookies: map[string]string{"SESSDATA": "cookie-secret", "bili_jct": "csrf-secret"}, AccountUID: "42", AccountName: "User", UpdatedAt: time.Unix(1000, 0), RefreshToken: "refresh-secret", PendingRefreshToken: tt.pending}
			require.NoError(t, store.SaveSession(original))
			if tt.legacy {
				sealed, err := sealJSON(v, tablePlatformAccounts, string(model.PlatformBilibili), original.Cookies)
				require.NoError(t, err)
				require.NoError(t, store.db.Model(&platformAccountRow{}).Where("platform = ?", model.PlatformBilibili).Update("sealed_session", sealed).Error)
				original.RefreshToken = ""
			}
			var row platformAccountRow
			require.NoError(t, store.db.Where("platform = ?", model.PlatformBilibili).Take(&row).Error)
			assert.NotContains(t, string(row.SealedSession), "secret")
			got, err := store.Session()
			require.NoError(t, err)
			assert.WithinDuration(t, time.Now(), got.UpdatedAt, 5*time.Second)
			original.UpdatedAt = got.UpdatedAt
			assert.Equal(t, original, got)
			public, err := store.ListPlatformAccounts()
			require.NoError(t, err)
			data, err := json.Marshal(public)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "secret")
			account, err := store.PlatformAccount(model.PlatformBilibili)
			require.NoError(t, err)
			assert.Equal(t, original.Cookies, account.Session)
			require.NoError(t, store.Close())
			reopened, err := Open(t.Context(), path, v)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			got, err = reopened.Session()
			require.NoError(t, err)
			assert.Equal(t, original, got)
			require.NoError(t, reopened.ClearSession())
			_, err = reopened.Session()
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestBiliSessionRejectsCorruptPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload any
	}{
		{name: "future version", payload: map[string]any{"version": 2, "cookies": map[string]string{"SESSDATA": "secret"}}},
		{name: "invalid cookies", payload: map[string]any{"version": 1, "cookies": 42}},
		{name: "invalid legacy", payload: map[string]any{"SESSDATA": 42}},
		{name: "array", payload: []string{"secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 42))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "42", Cookies: map[string]string{"SESSDATA": "old"}}))
			sealed, err := sealJSON(store.vault, tablePlatformAccounts, string(model.PlatformBilibili), tt.payload)
			require.NoError(t, err)
			require.NoError(t, store.db.Model(&platformAccountRow{}).Where("platform = ?", model.PlatformBilibili).Update("sealed_session", sealed).Error)
			_, err = store.Session()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
			_, err = store.PlatformAccount(model.PlatformBilibili)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
}

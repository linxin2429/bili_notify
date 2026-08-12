package state

import (
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T, fill byte) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, fill))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func putEnabledTestChannel(t *testing.T, store *Store) model.Channel {
	t.Helper()
	channel, err := store.PutChannel(model.Channel{
		Name:     "channel",
		Type:     model.ChannelWeCom,
		Enabled:  true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)
	return channel
}

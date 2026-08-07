package web

import (
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPasswordHash(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.True(t, verifyPassword(hash, "correct horse battery staple"))
	assert.False(t, verifyPassword(hash, "wrong password"))
}

func TestShortPasswordRejected(t *testing.T) {
	t.Parallel()
	_, err := HashPassword("short")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "12 bytes")
}

func TestAdministratorInitializationPersists(t *testing.T) {
	t.Parallel()
	v, err := vault.New(make([]byte, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NotEmpty(t, setupCode)

	require.Error(t, auth.initialize("WRONG", "correct horse battery staple"))
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	assert.True(t, auth.authenticate("correct horse battery staple"))

	reopened, nextCode, err := newAuthenticator(store)
	require.NoError(t, err)
	assert.Empty(t, nextCode)
	assert.True(t, reopened.authenticate("correct horse battery staple"))
}

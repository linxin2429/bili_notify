package web

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateTLSConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tls.pem")
	config, err := loadOrCreateTLSConfig(path)
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), config.MinVersion)
	require.Len(t, config.Certificates, 1)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	second, err := loadOrCreateTLSConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Certificates[0].Leaf.SerialNumber.Cmp(config.Certificates[0].Leaf.SerialNumber))
}

func TestInvalidTLSBundleRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tls.pem")
	require.NoError(t, os.WriteFile(path, []byte("invalid"), 0o600))
	_, err := loadOrCreateTLSConfig(path)
	require.Error(t, err)
}

func TestTLSBundleWithBroadPermissionsRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tls.pem")
	_, err := loadOrCreateTLSConfig(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = loadOrCreateTLSConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want 600")
}

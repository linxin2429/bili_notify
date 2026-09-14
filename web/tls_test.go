package web

import (
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestTLSFileFailuresPreserveExistingData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		directory bool
		write     bool
		child     bool
		want      string
	}{
		{name: "read directory", directory: true, want: "reading TLS bundle"},
		{name: "existing bundle", write: true, want: "creating TLS bundle"},
		{name: "parent is file", write: true, child: true, want: "creating TLS directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "existing")
			if tt.directory {
				require.NoError(t, os.Mkdir(path, 0o700))
			} else {
				require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))
			}
			target := path
			if tt.child {
				target = filepath.Join(path, "tls.pem")
			}
			var err error
			if tt.write {
				err = writeExclusive(target, []byte("replacement"), 0o600)
			} else {
				_, err = loadOrCreateTLSConfig(target)
			}
			require.ErrorContains(t, err, tt.want)
			if !tt.directory {
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "original", string(got))
			}
		})
	}
}

func TestTLSCertificateValidityWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		start, end time.Duration
	}{{"expired", -48 * time.Hour, -24 * time.Hour}, {"not yet valid", 24 * time.Hour, 48 * time.Hour}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle, err := generateSelfSignedBundle()
			require.NoError(t, err)
			pair, err := tls.X509KeyPair(bundle, bundle)
			require.NoError(t, err)
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			require.NoError(t, err)
			leaf.NotBefore, leaf.NotAfter = time.Now().Add(tt.start), time.Now().Add(tt.end)
			signer, ok := pair.PrivateKey.(crypto.Signer)
			require.True(t, ok)
			der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, signer.Public(), signer)
			require.NoError(t, err)
			keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
			require.NoError(t, err)
			raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
			path := filepath.Join(t.TempDir(), "tls.pem")
			require.NoError(t, os.WriteFile(path, raw, 0o600))
			config, err := loadOrCreateTLSConfig(path)
			require.ErrorContains(t, err, "not currently valid")
			assert.Nil(t, config)
		})
	}
}

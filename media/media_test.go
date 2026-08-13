package media

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde,
}

func TestMediaFilesystemRejectsSymbolicLinkEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, string, string) error
	}{
		{
			name: "read through media directory link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.png"), testPNG, 0o600))
				require.NoError(t, os.Symlink(outside, filepath.Join(dataDir, "media")))
				_, _, err := ReadFile(dataDir, "media/secret.png")
				return err
			},
		},
		{
			name: "read through file link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "media", "1"), 0o700))
				secret := filepath.Join(outside, "secret.png")
				require.NoError(t, os.WriteFile(secret, testPNG, 0o600))
				require.NoError(t, os.Symlink(secret, filepath.Join(dataDir, "media", "1", "0.png")))
				_, _, err := ReadFile(dataDir, "media/1/0.png")
				return err
			},
		},
		{
			name: "remove through parent link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.MkdirAll(filepath.Join(outside, "42"), 0o700))
				require.NoError(t, os.Symlink(outside, filepath.Join(dataDir, "media")))
				return RemoveUP(dataDir, "42")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			outside := t.TempDir()
			err := tt.run(t, dataDir, outside)
			require.Error(t, err)
			assert.True(t, errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "symbolic link"), err.Error())
			assert.DirExists(t, outside)
		})
	}
}

func TestRemoteMediaURLSecurityPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rawURL       string
		allowPrivate bool
		wantErr      bool
	}{
		{name: "loopback IPv4", rawURL: "http://127.0.0.1/image.png", wantErr: true},
		{name: "loopback IPv6", rawURL: "http://[::1]/image.png", wantErr: true},
		{name: "private address", rawURL: "http://10.0.0.1/image.png", wantErr: true},
		{name: "link local address", rawURL: "http://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "unsupported scheme", rawURL: "file:///etc/passwd", wantErr: true},
		{name: "explicit private-network client", rawURL: "http://127.0.0.1/image.png", allowPrivate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target, err := url.Parse(tt.rawURL)
			require.NoError(t, err)
			err = validateRemoteURL(t.Context(), target, tt.allowPrivate)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResolveAndRemoveUP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{name: "valid", rel: "media/1/2/0.jpg"},
		{name: "escape", rel: "media/../secret", wantErr: true},
		{name: "outside", rel: "other/1.jpg", wantErr: true},
		{name: "empty", rel: "", wantErr: true},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			abs, err := Resolve(dir, tt.rel)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(abs, filepath.Clean(dir)))
		})
	}

	path := filepath.Join(dir, filepath.FromSlash(relativePath("42", "9", 0, ".jpg")))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	require.NoError(t, RemoveUP(dir, "42"))
	_, err := os.Stat(filepath.Dir(filepath.Dir(path)))
	assert.True(t, os.IsNotExist(err))
}

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	valid := Config{
		DataDir: "/data", AdminAddr: ":8443", ObserveAddr: ":9090", PollInterval: 30 * time.Second,
		RequestRate: 2, RequestConcurrency: 4, LogLevel: "info",
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid"},
		{
			name:    "unknown log level",
			mutate:  func(c *Config) { c.LogLevel = "verbose" },
			wantErr: "log level",
		},
		{
			name:    "missing data dir",
			mutate:  func(c *Config) { c.DataDir = "" },
			wantErr: "data directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadOrCreateMasterKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := LoadOrCreateMasterKey(dir)
	require.NoError(t, err)
	require.Len(t, first, 32)

	second, err := LoadOrCreateMasterKey(dir)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	_, keyPath, _ := Paths(dir)
	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestDatabaseWithoutKeyRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, StateFileName), []byte("state"), 0o600))
	_, err := LoadOrCreateMasterKey(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "master key")
}

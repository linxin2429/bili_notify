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
		AuditLogRetention: 180 * 24 * time.Hour,
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
		{
			name:    "sub-day audit retention",
			mutate:  func(c *Config) { c.AuditLogRetention = 12 * time.Hour },
			wantErr: "audit log retention",
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

func TestSeedRuntimeSettings(t *testing.T) {
	t.Parallel()
	cfg := Config{
		PollInterval: 45 * time.Second, RequestRate: 1.5, RequestConcurrency: 3, LogLevel: " WARN ",
		AuditLogRetention: 90 * 24 * time.Hour,
	}
	settings := cfg.SeedRuntimeSettings()
	require.NoError(t, settings.Validate())
	assert.Equal(t, 45, settings.PollIntervalSec)
	assert.Equal(t, 1.5, settings.RequestRate)
	assert.Equal(t, 3, settings.RequestConcurrency)
	assert.Equal(t, "warn", settings.LogLevel)
	assert.Equal(t, 90, settings.AuditLogRetentionDays)
	assert.Equal(t, 10, settings.MaxDynamicPages)
	assert.Equal(t, [5]int{5, 30, 120, 600, 3600}, [5]int(settings.DeliveryRetryDelaysSec))
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, DataFileName), []byte("state"), 0o600))
	_, err := LoadOrCreateMasterKey(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "master key")
}

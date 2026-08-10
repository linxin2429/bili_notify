package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	v := mustVault(t, 30)
	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRefuseLegacyDataDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   []string
		wantErr string
	}{
		{name: "empty", files: nil},
		{name: "data only", files: []string{config.DataFileName}},
		{name: "state only", files: []string{config.LegacyStateFile}, wantErr: "state.db"},
		{name: "content only", files: []string{config.LegacyContentFile}, wantErr: "content.db"},
		{name: "both legacy", files: []string{config.LegacyStateFile, config.LegacyContentFile}, wantErr: "state.db"},
		{name: "data and legacy", files: []string{config.DataFileName, config.LegacyStateFile}, wantErr: "state.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, name := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
			}
			err := config.RefuseLegacyDataDir(dir)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "fresh data directory")
		})
	}
}

func TestRunMigrationsCreatesTables(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, runMigrations(context.Background(), db))
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ups'`).Scan(&name))
	assert.Equal(t, "ups", name)
	// second up is no-op
	require.NoError(t, runMigrations(context.Background(), db))
}

func TestRuntimeSettingsBooleanMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		old  bool
	}{
		{name: "enabled", old: true},
		{name: "disabled", old: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "data.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
			require.NoError(t, err)
			_, err = provider.UpTo(t.Context(), 7)
			require.NoError(t, err)

			oldRecord, err := json.Marshal(map[string]any{
				"_version":        2,
				"comment_enabled": tt.old,
			})
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO meta(key, value) VALUES ('runtime_settings', ?)`, oldRecord)
			require.NoError(t, err)
			_, err = provider.UpTo(t.Context(), 8)
			require.NoError(t, err)

			var brokenType string
			require.NoError(t, db.QueryRow(`SELECT json_type(value, '$.bilibili_comments_enabled') FROM meta WHERE key = 'runtime_settings'`).Scan(&brokenType))
			assert.Equal(t, "integer", brokenType)

			_, err = provider.Up(t.Context())
			require.NoError(t, err)
			var raw string
			require.NoError(t, db.QueryRow(`SELECT value FROM meta WHERE key = 'runtime_settings'`).Scan(&raw))
			var record runtimeSettingsRecord
			require.NoError(t, json.Unmarshal([]byte(raw), &record))
			assert.Equal(t, tt.old, record.BilibiliCommentsEnabled)
		})
	}
}

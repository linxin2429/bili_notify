package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenWritesStructuredCategoriesToBothSinks(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), "logs", "bili-notify.jsonl")
	loggers, err := Open(Config{Level: "debug", Version: "1.2.3", FilePath: path, Retention: 30 * 24 * time.Hour, Stdout: &stdout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = loggers.Close() })

	loggers.System.Info("started", "event", "process.start")
	loggers.Audit.Info("changed", "event", "administrator.operation")
	require.NoError(t, loggers.Close())

	fileData, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, stdout.String(), string(fileData))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	scanner := bufio.NewScanner(bytes.NewReader(fileData))
	categories := make([]string, 0, 2)
	for scanner.Scan() {
		var record map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		categories = append(categories, record["category"].(string))
		assert.Equal(t, "bili-notify", record["service"])
		assert.Equal(t, "1.2.3", record["version"])
		assert.Equal(t, float64(1), record["log_schema"])
		assert.NotEmpty(t, record["run_id"])
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, []string{"system", "audit"}, categories)
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "debug", value: "debug"},
		{name: "info", value: "INFO"},
		{name: "warn", value: " warn "},
		{name: "error", value: "error"},
		{name: "invalid", value: "trace", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseLevel(tt.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSetApplyUpdatesLevelAndRetention(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	loggers, err := Open(Config{
		Level: "debug", Version: "test", FilePath: filepath.Join(t.TempDir(), "app.jsonl"),
		Retention: 30 * 24 * time.Hour, Stdout: &stdout,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = loggers.Close() })

	require.NoError(t, loggers.Apply("warn", 90*24*time.Hour))
	loggers.System.Info("hidden")
	loggers.System.Error("visible")
	assert.NotContains(t, stdout.String(), "hidden")
	assert.Contains(t, stdout.String(), "visible")
	assert.Equal(t, 90*24*time.Hour, loggers.sink.retention)
}

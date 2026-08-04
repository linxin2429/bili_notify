package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigRejectsUnknownLogLevel(t *testing.T) {
	config := Config{
		DataDir: "/data", AdminAddr: ":8443", ObserveAddr: ":9090", PollInterval: 30 * time.Second,
		RequestRate: 2, RequestConcurrency: 4, LogLevel: "verbose",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("unknown log level was accepted")
	}
}

func TestLoadOrCreateMasterKey(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || len(first) != 32 {
		t.Fatal("master key was not reused")
	}
	_, keyPath, _ := Paths(dir)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode=%o, want 600", info.Mode().Perm())
	}
}

func TestDatabaseWithoutKeyRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(dir); err == nil {
		t.Fatal("database without key was accepted")
	}
}

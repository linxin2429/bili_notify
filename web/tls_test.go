package web

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateTLSConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tls.pem")
	config, err := loadOrCreateTLSConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || len(config.Certificates) != 1 {
		t.Fatalf("unexpected TLS config: %#v", config)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("TLS bundle mode=%o, want 600", info.Mode().Perm())
	}
	second, err := loadOrCreateTLSConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if second.Certificates[0].Leaf.SerialNumber.Cmp(config.Certificates[0].Leaf.SerialNumber) != 0 {
		t.Fatal("existing TLS certificate was not reused")
	}
}

func TestInvalidTLSBundleRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tls.pem")
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTLSConfig(path); err == nil {
		t.Fatal("invalid TLS bundle was accepted")
	}
}

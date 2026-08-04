package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	StateFileName     = "state.db"
	MasterKeyFileName = "master.key"
	TLSFileName       = "tls.pem"
)

// Config contains immutable, startup-only settings. Runtime settings live in bbolt.
type Config struct {
	DataDir            string        `mapstructure:"data_dir"`
	AdminAddr          string        `mapstructure:"admin_addr"`
	ObserveAddr        string        `mapstructure:"observe_addr"`
	PollInterval       time.Duration `mapstructure:"poll_interval"`
	RequestRate        float64       `mapstructure:"request_rate"`
	RequestConcurrency int           `mapstructure:"request_concurrency"`
	LogLevel           string        `mapstructure:"log_level"`
}

func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.DataDir) == "" {
		errs = append(errs, errors.New("data directory is required"))
	}
	for name, addr := range map[string]string{"admin": c.AdminAddr, "observability": c.ObserveAddr} {
		if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
			errs = append(errs, fmt.Errorf("invalid %s address %q: %w", name, addr, err))
		}
	}
	if c.PollInterval < 10*time.Second {
		errs = append(errs, errors.New("poll interval must be at least 10s"))
	}
	if c.RequestRate <= 0 || c.RequestRate > 10 {
		errs = append(errs, errors.New("request rate must be in (0, 10]"))
	}
	if c.RequestConcurrency < 1 || c.RequestConcurrency > 16 {
		errs = append(errs, errors.New("request concurrency must be in [1, 16]"))
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("invalid log level %q", c.LogLevel))
	}
	return errors.Join(errs...)
}

func Paths(dataDir string) (statePath, keyPath, tlsPath string) {
	return filepath.Join(dataDir, StateFileName), filepath.Join(dataDir, MasterKeyFileName), filepath.Join(dataDir, TLSFileName)
}

// LoadOrCreateMasterKey returns the installation key, creating it for a new data directory.
func LoadOrCreateMasterKey(dataDir string) ([]byte, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("securing data directory: %w", err)
	}
	statePath, keyPath, _ := Paths(dataDir)
	key, err := os.ReadFile(keyPath)
	if err == nil {
		info, statErr := os.Stat(keyPath)
		if statErr != nil {
			return nil, fmt.Errorf("checking master key permissions: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("master key permissions are %o, want 600", info.Mode().Perm())
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("master key has %d bytes, want 32", len(key))
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading master key: %w", err)
	}
	for _, databasePath := range []string{statePath, filepath.Join(dataDir, "bili-notify.db")} {
		if _, err := os.Stat(databasePath); err == nil {
			return nil, errors.New("state database exists without its master key; use a fresh data directory")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("checking state database: %w", err)
		}
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating master key: %w", err)
	}
	file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating master key: %w", err)
	}
	writeErr := error(nil)
	if _, err := file.Write(key); err != nil {
		writeErr = fmt.Errorf("writing master key: %w", err)
	} else if err := file.Sync(); err != nil {
		writeErr = fmt.Errorf("syncing master key: %w", err)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing master key: %w", closeErr)
	}
	return key, nil
}

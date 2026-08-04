package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Config contains immutable, startup-only settings. Runtime settings live in bbolt.
type Config struct {
	DataPath           string        `mapstructure:"data_path"`
	AdminAddr          string        `mapstructure:"admin_addr"`
	ObserveAddr        string        `mapstructure:"observe_addr"`
	TLSCertFile        string        `mapstructure:"tls_cert_file"`
	TLSKeyFile         string        `mapstructure:"tls_key_file"`
	MasterKeyFile      string        `mapstructure:"master_key_file"`
	AdminHashFile      string        `mapstructure:"admin_hash_file"`
	PollInterval       time.Duration `mapstructure:"poll_interval"`
	RequestRate        float64       `mapstructure:"request_rate"`
	RequestConcurrency int           `mapstructure:"request_concurrency"`
	LogLevel           string        `mapstructure:"log_level"`
}

func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.DataPath) == "" {
		errs = append(errs, errors.New("data path is required"))
	}
	for name, addr := range map[string]string{"admin": c.AdminAddr, "observability": c.ObserveAddr} {
		if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
			errs = append(errs, fmt.Errorf("invalid %s address %q: %w", name, addr, err))
		}
	}
	for name, path := range map[string]string{
		"TLS certificate": c.TLSCertFile,
		"TLS private key": c.TLSKeyFile,
		"master key":      c.MasterKeyFile,
		"admin hash":      c.AdminHashFile,
	} {
		if strings.TrimSpace(path) == "" {
			errs = append(errs, fmt.Errorf("%s file is required", name))
			continue
		}
		if _, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("reading %s file: %w", name, err))
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
	return errors.Join(errs...)
}

func ReadSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading secret file %s: %w", path, err)
	}
	return []byte(strings.TrimSpace(string(b))), nil
}

func ReadMasterKey(path string) ([]byte, error) {
	raw, err := ReadSecret(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("decoding base64 master key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key has %d bytes, want 32", len(key))
	}
	return key, nil
}

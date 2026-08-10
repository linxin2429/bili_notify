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

	"github.com/linxin2429/bili_notify/model"
)

const (
	DataFileName          = "data.db"
	LegacyStateFile       = "state.db"
	LegacyContentFile     = "content.db"
	MasterKeyFileName     = "master.key"
	TLSFileName           = "tls.pem"
	MediaDirName          = "media"
	DefaultAIWorkerSocket = "/run/bili-notify/ai-worker.sock"
)

// Config contains process startup settings.
// DataDir, listen addresses, and OpenTelemetry values are immutable for the process
// lifetime. Collector, logging, and retention values are first-run defaults only:
// when the store has no runtime settings yet they seed it; afterwards the admin UI
// owns them.
type Config struct {
	DataDir                         string        `mapstructure:"data_dir"`
	AdminAddr                       string        `mapstructure:"admin_addr"`
	ObserveAddr                     string        `mapstructure:"observe_addr"`
	AIWorkerSocket                  string        `mapstructure:"ai_worker_socket"`
	PollInterval                    time.Duration `mapstructure:"poll_interval"`
	RequestRate                     float64       `mapstructure:"request_rate"`
	RequestConcurrency              int           `mapstructure:"request_concurrency"`
	LogLevel                        string        `mapstructure:"log_level"`
	AuditLogRetention               time.Duration `mapstructure:"audit_log_retention"`
	OTelSDKDisabled                 bool          `mapstructure:"otel_sdk_disabled"`
	OTelServiceName                 string        `mapstructure:"otel_service_name"`
	OTelDeploymentEnvironment       string        `mapstructure:"otel_deployment_environment"`
	OTelExporterOTLPEndpoint        string        `mapstructure:"otel_exporter_otlp_endpoint"`
	OTelExporterOTLPProtocol        string        `mapstructure:"otel_exporter_otlp_protocol"`
	OTelExporterOTLPTracesProtocol  string        `mapstructure:"otel_exporter_otlp_traces_protocol"`
	OTelExporterOTLPMetricsProtocol string        `mapstructure:"otel_exporter_otlp_metrics_protocol"`
	OTelExporterOTLPLogsProtocol    string        `mapstructure:"otel_exporter_otlp_logs_protocol"`
	OTelMetricExportInterval        time.Duration `mapstructure:"otel_metric_export_interval"`
}

func (c Config) EffectiveAIWorkerSocket() string {
	if value := strings.TrimSpace(c.AIWorkerSocket); value != "" {
		return value
	}
	return DefaultAIWorkerSocket
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
	if err := model.ValidateCollectorParams(c.PollInterval, c.RequestRate, c.RequestConcurrency); err != nil {
		errs = append(errs, err)
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("invalid log level %q", c.LogLevel))
	}
	if retention := c.AuditLogRetention; retention < 24*time.Hour || retention > 3650*24*time.Hour || retention%(24*time.Hour) != 0 {
		errs = append(errs, errors.New("audit log retention must be a whole number of days between 1 and 3650"))
	}
	return errors.Join(errs...)
}

// SeedRuntimeSettings converts startup defaults into a complete runtime settings record.
func (c Config) SeedRuntimeSettings() model.RuntimeSettings {
	settings := model.DefaultRuntimeSettings()
	settings.PollIntervalSec = int(c.PollInterval / time.Second)
	settings.RequestRate = c.RequestRate
	settings.RequestConcurrency = c.RequestConcurrency
	settings.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	settings.AuditLogRetentionDays = int(c.AuditLogRetention / (24 * time.Hour))
	return settings
}

// Paths returns data.db, master.key, and tls.pem under dataDir.
func Paths(dataDir string) (dataPath, keyPath, tlsPath string) {
	return filepath.Join(dataDir, DataFileName), filepath.Join(dataDir, MasterKeyFileName), filepath.Join(dataDir, TLSFileName)
}

// DataPath returns the SQLite database path under dataDir.
func DataPath(dataDir string) string {
	return filepath.Join(dataDir, DataFileName)
}

// MediaDir returns the on-disk media archive directory under dataDir.
func MediaDir(dataDir string) string {
	return filepath.Join(dataDir, MediaDirName)
}

// RefuseLegacyDataDir fails closed when an older bbolt/content dual-store volume is present.
// This version only supports a fresh data directory with data.db (no automatic import).
func RefuseLegacyDataDir(dataDir string) error {
	var found []string
	for _, name := range []string{LegacyStateFile, LegacyContentFile} {
		path := filepath.Join(dataDir, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking legacy database %s: %w", name, err)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("legacy %s found in %s; this version only supports a fresh data directory with data.db (no automatic import)", strings.Join(found, " and "), dataDir)
}

// LoadOrCreateMasterKey returns the installation key, creating it for a new data directory.
func LoadOrCreateMasterKey(dataDir string) ([]byte, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("securing data directory: %w", err)
	}
	dataPath, keyPath, _ := Paths(dataDir)
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
	for _, databasePath := range []string{
		dataPath,
		filepath.Join(dataDir, LegacyStateFile),
		filepath.Join(dataDir, LegacyContentFile),
		filepath.Join(dataDir, "bili-notify.db"),
	} {
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

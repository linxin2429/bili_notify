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
	BilibiliDynamicInterval         time.Duration `mapstructure:"bilibili_dynamic_interval"`
	BilibiliRequestRate             float64       `mapstructure:"bilibili_request_rate"`
	BilibiliRequestConcurrency      int           `mapstructure:"bilibili_request_concurrency"`
	ZSXQDynamicInterval             time.Duration `mapstructure:"zsxq_dynamic_interval"`
	ZSXQCommentInterval             time.Duration `mapstructure:"zsxq_comment_interval"`
	ZSXQRequestRate                 float64       `mapstructure:"zsxq_request_rate"`
	ZSXQRequestConcurrency          int           `mapstructure:"zsxq_request_concurrency"`
	ZSXQRiskPause                   time.Duration `mapstructure:"zsxq_risk_pause"`
	ZSXQAssetMaxFileSize            int64         `mapstructure:"zsxq_asset_max_file_size"`
	ZSXQAssetTotalBudget            int64         `mapstructure:"zsxq_asset_total_budget"`
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
	biliInterval, biliRate, biliConcurrency := c.bilibiliSeed()
	if err := model.ValidateCollectorParams(biliInterval, biliRate, biliConcurrency); err != nil {
		errs = append(errs, err)
	}
	zsxqDynamic, zsxqComment, zsxqRate, zsxqConcurrency, zsxqPause, maxFile, totalBudget := c.zsxqSeed()
	if zsxqDynamic < 30*time.Second || zsxqDynamic > 24*time.Hour {
		errs = append(errs, errors.New("ZSXQ dynamic interval must be between 30s and 24h"))
	}
	if zsxqComment < 30*time.Second || zsxqComment > 24*time.Hour {
		errs = append(errs, errors.New("ZSXQ comment interval must be between 30s and 24h"))
	}
	if zsxqRate <= 0 || zsxqRate > model.MaxRequestRate {
		errs = append(errs, errors.New("ZSXQ request rate must be in (0, 10]"))
	}
	if zsxqConcurrency < 1 || zsxqConcurrency > 16 {
		errs = append(errs, errors.New("ZSXQ request concurrency must be in [1, 16]"))
	}
	if zsxqPause < time.Minute || zsxqPause > time.Hour {
		errs = append(errs, errors.New("ZSXQ risk pause must be between 1m and 1h"))
	}
	if maxFile < 1<<20 || maxFile > 2048<<20 {
		errs = append(errs, errors.New("ZSXQ asset max file size must be between 1MiB and 2048MiB"))
	}
	if totalBudget < 1<<30 || totalBudget > int64(10240)<<30 {
		errs = append(errs, errors.New("ZSXQ asset total budget must be between 1GiB and 10240GiB"))
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
	biliInterval, biliRate, biliConcurrency := c.bilibiliSeed()
	settings.BilibiliDynamicIntervalSec = int(biliInterval / time.Second)
	settings.BilibiliRequestRate = biliRate
	settings.BilibiliRequestConcurrency = biliConcurrency
	zsxqDynamic, zsxqComment, zsxqRate, zsxqConcurrency, zsxqPause, maxFile, totalBudget := c.zsxqSeed()
	settings.ZSXQDynamicIntervalSec = int(zsxqDynamic / time.Second)
	settings.ZSXQCommentIntervalSec = int(zsxqComment / time.Second)
	settings.ZSXQRequestRate = zsxqRate
	settings.ZSXQRequestConcurrency = zsxqConcurrency
	settings.ZSXQRiskPauseSec = int(zsxqPause / time.Second)
	settings.ZSXQAssetMaxFileMiB = int(maxFile >> 20)
	settings.ZSXQAssetTotalBudgetGiB = int(totalBudget >> 30)
	settings.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	settings.AuditLogRetentionDays = int(c.AuditLogRetention / (24 * time.Hour))
	return settings
}

func (c Config) bilibiliSeed() (time.Duration, float64, int) {
	return c.BilibiliDynamicInterval, c.BilibiliRequestRate, c.BilibiliRequestConcurrency
}

func (c Config) zsxqSeed() (time.Duration, time.Duration, float64, int, time.Duration, int64, int64) {
	dynamic, comments, requestRate, concurrency := c.ZSXQDynamicInterval, c.ZSXQCommentInterval, c.ZSXQRequestRate, c.ZSXQRequestConcurrency
	pause, maxFile, totalBudget := c.ZSXQRiskPause, c.ZSXQAssetMaxFileSize, c.ZSXQAssetTotalBudget
	if dynamic == 0 {
		dynamic = time.Duration(model.DefaultZSXQDynamicIntervalSec) * time.Second
	}
	if comments == 0 {
		comments = time.Duration(model.DefaultZSXQCommentIntervalSec) * time.Second
	}
	if requestRate == 0 {
		requestRate = model.DefaultZSXQRequestRate
	}
	if concurrency == 0 {
		concurrency = model.DefaultZSXQRequestConcurrency
	}
	if pause == 0 {
		pause = time.Duration(model.DefaultZSXQRiskPauseSec) * time.Second
	}
	if maxFile == 0 {
		maxFile = int64(model.DefaultZSXQAssetMaxFileMiB) << 20
	}
	if totalBudget == 0 {
		totalBudget = int64(model.DefaultZSXQAssetTotalBudgetGiB) << 30
	}
	return dynamic, comments, requestRate, concurrency, pause, maxFile, totalBudget
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

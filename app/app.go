package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/logging"
	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/telemetry"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/linxin2429/bili_notify/web"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

// Dependencies contains process-boundary implementations used by RunWithDependencies.
// Zero values select the production implementations used by Run.
type Dependencies struct {
	BilibiliHTTPClient     *http.Client
	NotificationHTTPClient *http.Client
	BilibiliAPIURL         string
	BilibiliPassportURL    string
	Logger                 *slog.Logger
	AuditLogger            *slog.Logger
	AdminListener          net.Listener
	ObserveListener        net.Listener
	Telemetry              *telemetry.Runtime
}

func Run(ctx context.Context, cfg config.Config, version string) error {
	return RunWithDependencies(ctx, cfg, version, Dependencies{})
}

// RunWithDependencies runs the application with explicitly supplied process boundaries.
func RunWithDependencies(ctx context.Context, cfg config.Config, version string, dependencies Dependencies) (runErr error) {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := dependencies.validate(); err != nil {
		return err
	}
	if err := config.RefuseLegacyDataDir(cfg.DataDir); err != nil {
		return err
	}
	telemetryRuntime := dependencies.Telemetry
	if telemetryRuntime == nil {
		var err error
		telemetryRuntime, err = telemetry.New(ctx, telemetry.Config{
			Disabled: cfg.OTelSDKDisabled, ServiceName: cfg.OTelServiceName, DeploymentEnvironment: cfg.OTelDeploymentEnvironment,
			Endpoint: cfg.OTelExporterOTLPEndpoint, Protocol: cfg.OTelExporterOTLPProtocol,
			TracesProtocol: cfg.OTelExporterOTLPTracesProtocol, MetricsProtocol: cfg.OTelExporterOTLPMetricsProtocol, LogsProtocol: cfg.OTelExporterOTLPLogsProtocol,
			MetricExportInterval: cfg.OTelMetricExportInterval,
		}, version)
		if err != nil {
			return fmt.Errorf("initializing OpenTelemetry: %w", err)
		}
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := telemetryRuntime.Shutdown(shutdownCtx); err != nil {
			slog.New(slog.NewJSONHandler(os.Stderr, nil)).Warn("OpenTelemetry shutdown failed", "error", err)
		}
	}()
	logger := dependencies.Logger
	auditLogger := dependencies.AuditLogger
	var loggers *logging.Set
	if logger == nil {
		var err error
		loggers, err = logging.Open(logging.Config{
			Level: cfg.LogLevel, Version: version, Stdout: os.Stdout,
			RunID: telemetryRuntime.InstanceID, Provider: telemetryRuntime.LoggerProvider,
		})
		if err != nil {
			return fmt.Errorf("initializing structured logging: %w", err)
		}
		defer loggers.Close()
		logger = loggers.System
		auditLogger = loggers.Audit
	}
	if auditLogger == nil {
		auditLogger = logger.With("category", "audit")
	}
	appLogger := logger.With("component", "app")
	key, err := config.LoadOrCreateMasterKey(cfg.DataDir)
	if err != nil {
		return err
	}
	v, err := vault.New(key)
	if err != nil {
		return err
	}
	dataPath, _, tlsPath := config.Paths(cfg.DataDir)
	store, err := state.Open(ctx, dataPath, v, telemetryRuntime.TracerProvider)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.ListChannels(); err != nil {
		return fmt.Errorf("validating encrypted channel configuration: %w", err)
	}
	if _, err := store.Session(); err != nil && !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("validating encrypted Bilibili session: %w", err)
	}

	settings, err := store.RuntimeSettings()
	if errors.Is(err, state.ErrNotFound) {
		settings = cfg.SeedRuntimeSettings()
		if err := store.PutRuntimeSettings(settings); err != nil {
			return fmt.Errorf("seeding runtime settings: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("loading runtime settings: %w", err)
	}
	if loggers != nil {
		if err := loggers.Apply(settings.LogLevel); err != nil {
			return fmt.Errorf("applying stored logging settings: %w", err)
		}
	}

	appLogger.InfoContext(ctx, "Bili Notify starting",
		"event", "process.start",
		"version", version,
		"timezone", time.Local.String(),
		"poll_interval_sec", settings.PollIntervalSec,
		"request_rate", settings.RequestRate,
		"request_concurrency", settings.RequestConcurrency,
		"admin_addr", cfg.AdminAddr,
		"observe_addr", cfg.ObserveAddr,
	)
	httpClient := dependencies.BilibiliHTTPClient
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	var clientOptions []bilibili.Option
	if dependencies.BilibiliAPIURL != "" {
		clientOptions = append(clientOptions, bilibili.WithBaseURLs(dependencies.BilibiliAPIURL, dependencies.BilibiliPassportURL))
	}
	clientOptions = append(clientOptions, bilibili.WithTelemetry(telemetryRuntime.TracerProvider, telemetryRuntime.MeterProvider))
	client := bilibili.New(httpClient, "bili-notify/"+version, clientOptions...)
	if err := os.MkdirAll(config.MediaDir(cfg.DataDir), 0o700); err != nil {
		return fmt.Errorf("creating media directory: %w", err)
	}
	downloader := &media.Downloader{
		DataDir:   cfg.DataDir,
		Client:    httpClient,
		UserAgent: "bili-notify/" + version,
		Tracer:    telemetryRuntime.TracerProvider.Tracer("github.com/linxin2429/bili_notify/media"),
	}
	metrics := service.NewMetrics(telemetryRuntime.MeterProvider)
	events := service.NewEventBus()
	var engineOptions []service.EngineOption
	if dependencies.NotificationHTTPClient != nil {
		engineOptions = append(engineOptions, service.WithNotificationHTTPClient(dependencies.NotificationHTTPClient))
	}
	engineOptions = append(engineOptions, service.WithTracerProvider(telemetryRuntime.TracerProvider))
	engine := service.NewEngine(store, client, logger.With("component", "engine"), metrics, settings, events, downloader, engineOptions...)
	settingsManager := newRuntimeSettingsManager(store, engine, loggers, events)
	server, err := web.NewServer(cfg.AdminAddr, cfg.ObserveAddr, tlsPath, engine, settingsManager, store, events, logger.With("component", "web"), auditLogger.With("component", "web"), telemetryRuntime.TracerProvider, telemetryRuntime.MeterProvider, telemetryRuntime.Propagator, cfg.DataDir)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return engine.Run(gctx) })
	g.Go(func() error {
		return runAuditRetention(gctx, store, settingsManager.Settings, appLogger, telemetryRuntime.Tracer(), metrics)
	})
	if dependencies.AdminListener != nil {
		g.Go(func() error { return server.Serve(gctx, dependencies.AdminListener, dependencies.ObserveListener) })
	} else {
		g.Go(func() error { return server.Run(gctx) })
	}
	err = g.Wait()
	if err != nil {
		appLogger.ErrorContext(ctx, "Bili Notify stopped with an error", "event", "process.stop", "result", "failure", "error", err)
		return err
	}
	appLogger.InfoContext(ctx, "Bili Notify stopped", "event", "process.stop", "result", "success")
	return nil
}

func (d Dependencies) validate() error {
	if (d.BilibiliAPIURL == "") != (d.BilibiliPassportURL == "") {
		return errors.New("Bilibili API and passport base URLs must be provided together")
	}
	if (d.AdminListener == nil) != (d.ObserveListener == nil) {
		return errors.New("admin and observability listeners must be provided together")
	}
	return nil
}

func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          20,
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func runAuditRetention(ctx context.Context, store *state.Store, currentSettings func() model.RuntimeSettings, logger *slog.Logger, tracer trace.Tracer, metrics *service.Metrics) error {
	prune := func() {
		_ = pruneAuditRetentionOnce(ctx, store, currentSettings(), time.Now(), logger, tracer, metrics)
	}
	prune()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			prune()
		}
	}
}

// pruneAuditRetentionOnce performs one bounded-batch retention pass. Keeping the
// pass separate from its daily scheduler makes the deletion boundary and failure
// behavior testable without waiting for a wall-clock tick.
func pruneAuditRetentionOnce(ctx context.Context, store *state.Store, settings model.RuntimeSettings, now time.Time, logger *slog.Logger, tracer trace.Tracer, metrics *service.Metrics) (err error) {
	pruneCtx, span := tracer.Start(ctx, "audit.retention")
	started := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "error"
			span.SetStatus(codes.Error, "audit retention failed")
		}
		metrics.RecordWorkflow(pruneCtx, "audit_retention", result, time.Since(started))
		span.End()
	}()

	contextStore := store.WithContext(pruneCtx)
	before := now.Add(-settings.AuditLogRetention())
	var deleted int64
	for {
		count, pruneErr := contextStore.PruneAuditLogs(before, 1000)
		if pruneErr != nil {
			logger.ErrorContext(pruneCtx, "audit log retention failed", "event", "audit.retention.failed", "error", pruneErr)
			return pruneErr
		}
		deleted += count
		if count < 1000 {
			break
		}
	}
	if deleted > 0 {
		logger.InfoContext(pruneCtx, "expired audit logs removed", "event", "audit.retention.completed", "deleted", deleted)
	}
	return nil
}

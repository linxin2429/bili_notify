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
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/linxin2429/bili_notify/web"
	"github.com/prometheus/client_golang/prometheus"
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
}

func Run(ctx context.Context, cfg config.Config, version string) error {
	return RunWithDependencies(ctx, cfg, version, Dependencies{})
}

// RunWithDependencies runs the application with explicitly supplied process boundaries.
func RunWithDependencies(ctx context.Context, cfg config.Config, version string, dependencies Dependencies) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := dependencies.validate(); err != nil {
		return err
	}
	if err := config.RefuseLegacyDataDir(cfg.DataDir); err != nil {
		return err
	}
	logger := dependencies.Logger
	auditLogger := dependencies.AuditLogger
	var loggers *logging.Set
	if logger == nil {
		var err error
		loggers, err = logging.Open(logging.Config{
			Level: cfg.LogLevel, Version: version, FilePath: config.LogPath(cfg.DataDir),
			Retention: cfg.SystemLogRetention, Stdout: os.Stdout,
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
	store, err := state.Open(dataPath, v)
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
	} else if err := settings.Validate(); err != nil {
		return fmt.Errorf("stored runtime settings are invalid: %w", err)
	}

	appLogger.Info("Bili Notify starting",
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
	client := bilibili.New(httpClient, "bili-notify/"+version, clientOptions...)
	if err := os.MkdirAll(config.MediaDir(cfg.DataDir), 0o700); err != nil {
		return fmt.Errorf("creating media directory: %w", err)
	}
	downloader := &media.Downloader{
		DataDir:   cfg.DataDir,
		Client:    httpClient,
		UserAgent: "bili-notify/" + version,
	}
	registry := prometheus.NewRegistry()
	metrics := service.NewMetrics(registry)
	events := service.NewEventBus()
	var engineOptions []service.EngineOption
	if dependencies.NotificationHTTPClient != nil {
		engineOptions = append(engineOptions, service.WithNotificationHTTPClient(dependencies.NotificationHTTPClient))
	}
	engine := service.NewEngine(store, client, logger.With("component", "engine"), metrics, settings, events, downloader, engineOptions...)
	server, err := web.NewServer(cfg.AdminAddr, cfg.ObserveAddr, tlsPath, engine, store, events, logger.With("component", "web"), auditLogger.With("component", "web"), registry, cfg.DataDir)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return engine.Run(gctx) })
	g.Go(func() error { return runAuditRetention(gctx, store, cfg.AuditLogRetention, appLogger) })
	if dependencies.AdminListener != nil {
		g.Go(func() error { return server.Serve(gctx, dependencies.AdminListener, dependencies.ObserveListener) })
	} else {
		g.Go(func() error { return server.Run(gctx) })
	}
	err = g.Wait()
	if err != nil {
		appLogger.Error("Bili Notify stopped with an error", "event", "process.stop", "result", "failure", "error", err)
		return err
	}
	appLogger.Info("Bili Notify stopped", "event", "process.stop", "result", "success")
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

func runAuditRetention(ctx context.Context, store *state.Store, retention time.Duration, logger *slog.Logger) error {
	prune := func() {
		var deleted int64
		for {
			count, err := store.PruneAuditLogs(time.Now().Add(-retention), 1000)
			if err != nil {
				logger.Error("audit log retention failed", "event", "audit.retention.failed", "error", err)
				return
			}
			deleted += count
			if count < 1000 {
				break
			}
		}
		if deleted > 0 {
			logger.Info("expired audit logs removed", "event", "audit.retention.completed", "deleted", deleted)
		}
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

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/linxin2429/bili_notify/web"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
)

func Run(ctx context.Context, cfg config.Config, version string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	key, err := config.LoadOrCreateMasterKey(cfg.DataDir)
	if err != nil {
		return err
	}
	v, err := vault.New(key)
	if err != nil {
		return err
	}
	statePath, _, tlsPath := config.Paths(cfg.DataDir)
	store, err := state.Open(statePath, v)
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

	level := new(slog.LevelVar)
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		// slog's JSON handler rewrites time.Time to UTC; emit local wall-clock times instead.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindTime {
				return slog.String(a.Key, a.Value.Time().In(time.Local).Format(time.RFC3339Nano))
			}
			return a
		},
	}))
	logger.Info("Bili Notify starting",
		"version", version,
		"timezone", time.Local.String(),
		"poll_interval_sec", settings.PollIntervalSec,
		"request_rate", settings.RequestRate,
		"request_concurrency", settings.RequestConcurrency,
		"admin_addr", cfg.AdminAddr,
		"observe_addr", cfg.ObserveAddr,
	)
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          20,
	}
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	client := bilibili.New(httpClient, "bili-notify/"+version)
	registry := prometheus.NewRegistry()
	metrics := service.NewMetrics(registry)
	events := service.NewEventBus()
	engine := service.NewEngine(store, client, logger, metrics, settings, events)
	server, err := web.NewServer(cfg.AdminAddr, cfg.ObserveAddr, tlsPath, engine, store, events, logger, registry)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return engine.Run(gctx) })
	g.Go(func() error { return server.Run(gctx) })
	return g.Wait()
}

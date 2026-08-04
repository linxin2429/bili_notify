package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	key, err := config.ReadMasterKey(cfg.MasterKeyFile)
	if err != nil {
		return err
	}
	v, err := vault.New(key)
	if err != nil {
		return err
	}
	adminHash, err := config.ReadSecret(cfg.AdminHashFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DataPath), 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	store, err := state.Open(cfg.DataPath, v)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.ListChannels(false); err != nil {
		return fmt.Errorf("validating encrypted channel configuration: %w", err)
	}
	if _, err := store.Session(); err != nil && !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("validating encrypted Bilibili session: %w", err)
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
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
	engine := service.NewEngine(store, client, logger, metrics, cfg.PollInterval, cfg.RequestRate, cfg.RequestConcurrency)
	server, err := web.NewServer(cfg.AdminAddr, cfg.ObserveAddr, cfg.TLSCertFile, cfg.TLSKeyFile, string(adminHash), engine, store, logger, registry)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return engine.Run(gctx) })
	g.Go(func() error { return server.Run(gctx) })
	return g.Wait()
}

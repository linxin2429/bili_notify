package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestDependenciesValidate(t *testing.T) {
	t.Parallel()
	listener := new(net.TCPListener)
	tests := []struct {
		name    string
		value   Dependencies
		wantErr string
	}{
		{name: "defaults"},
		{name: "complete base URLs", value: Dependencies{BilibiliAPIURL: "https://api.example", BilibiliPassportURL: "https://passport.example"}},
		{name: "complete listeners", value: Dependencies{AdminListener: listener, ObserveListener: listener}},
		{name: "missing passport URL", value: Dependencies{BilibiliAPIURL: "https://api.example"}, wantErr: "base URLs"},
		{name: "missing API URL", value: Dependencies{BilibiliPassportURL: "https://passport.example"}, wantErr: "base URLs"},
		{name: "missing observability listener", value: Dependencies{AdminListener: listener}, wantErr: "listeners"},
		{name: "missing admin listener", value: Dependencies{ObserveListener: listener}, wantErr: "listeners"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRunWithDependenciesLifecycleAndPersistentSeed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	cfg := testConfig(dataDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec -- test-only self-signed server
	t.Cleanup(httpClient.CloseIdleConnections)

	for run := 1; run <= 2; run++ {
		admin := mustListen(t)
		observe := mustListen(t)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- RunWithDependencies(ctx, cfg, "test", Dependencies{
				Logger: logger, AuditLogger: logger, BilibiliHTTPClient: upstream.Client(), NotificationHTTPClient: upstream.Client(),
				BilibiliAPIURL: upstream.URL, BilibiliPassportURL: upstream.URL,
				AdminListener: admin, ObserveListener: observe,
			})
		}()

		require.Eventually(t, func() bool {
			response, err := http.Get("http://" + observe.Addr().String() + "/healthz")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 5*time.Second, 10*time.Millisecond)
		require.Eventually(t, func() bool {
			response, err := httpClient.Get("https://" + admin.Addr().String() + "/api/v2/session")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 5*time.Second, 10*time.Millisecond)

		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "application did not stop after cancellation", "run %d", run)
		}
	}

	key, err := os.ReadFile(filepath.Join(dataDir, config.MasterKeyFileName))
	require.NoError(t, err)
	v, err := vault.New(key)
	require.NoError(t, err)
	store, err := state.Open(t.Context(), config.DataPath(dataDir), v)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	settings, err := store.RuntimeSettings()
	require.NoError(t, err)
	assert.Equal(t, cfg.SeedRuntimeSettings(), settings)
}

func TestRunWithDependenciesRejectsUnsafeStartupState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*testing.T, string, *config.Config)
		wantErr string
	}{
		{
			name: "legacy database", wantErr: "legacy state.db",
			prepare: func(t *testing.T, dir string, _ *config.Config) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, config.LegacyStateFile), []byte("legacy"), 0o600))
			},
		},
		{
			name: "invalid TLS bundle", wantErr: "loading TLS bundle",
			prepare: func(t *testing.T, dir string, _ *config.Config) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, config.TLSFileName), []byte("invalid"), 0o600))
			},
		},
		{
			name: "channel encrypted by another key", wantErr: "validating encrypted channel",
			prepare: func(t *testing.T, dir string, _ *config.Config) {
				firstVault, err := vault.New(bytes.Repeat([]byte{1}, 32))
				require.NoError(t, err)
				store, err := state.Open(t.Context(), config.DataPath(dir), firstVault)
				require.NoError(t, err)
				_, err = store.PutChannel(model.Channel{Name: "channel", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.invalid"}})
				require.NoError(t, err)
				require.NoError(t, store.Close())
				require.NoError(t, os.WriteFile(filepath.Join(dir, config.MasterKeyFileName), bytes.Repeat([]byte{2}, 32), 0o600))
			},
		},
		{
			name: "session encrypted by another key", wantErr: "validating encrypted Bilibili session",
			prepare: func(t *testing.T, dir string, _ *config.Config) {
				firstVault, err := vault.New(bytes.Repeat([]byte{3}, 32))
				require.NoError(t, err)
				store, err := state.Open(t.Context(), config.DataPath(dir), firstVault)
				require.NoError(t, err)
				require.NoError(t, store.SaveSession(model.BiliSession{AccountUID: "42", Cookies: map[string]string{"SESSDATA": "secret"}}))
				require.NoError(t, store.Close())
				require.NoError(t, os.WriteFile(filepath.Join(dir, config.MasterKeyFileName), bytes.Repeat([]byte{4}, 32), 0o600))
			},
		},
		{
			name: "media path is not a directory", wantErr: "creating media directory",
			prepare: func(t *testing.T, dir string, _ *config.Config) {
				require.NoError(t, os.WriteFile(config.MediaDir(dir), []byte("not a directory"), 0o600))
			},
		},
		{
			name: "invalid telemetry protocol", wantErr: "OpenTelemetry",
			prepare: func(_ *testing.T, _ string, cfg *config.Config) {
				cfg.OTelSDKDisabled = false
				cfg.OTelExporterOTLPEndpoint = "http://127.0.0.1:4318"
				cfg.OTelExporterOTLPProtocol = "invalid"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cfg := testConfig(dir)
			tt.prepare(t, dir, &cfg)
			err := RunWithDependencies(t.Context(), cfg, "test", Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPruneAuditRetentionOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	settings := model.DefaultRuntimeSettings()
	settings.AuditLogRetentionDays = 1
	store := openTestStore(t)
	for index := 0; index < 1002; index++ {
		appendAudit(t, store, now.Add(-24*time.Hour-time.Millisecond), index)
	}
	appendAudit(t, store, now.Add(-24*time.Hour), 1002)
	appendAudit(t, store, now.Add(-23*time.Hour), 1003)

	err := pruneAuditRetentionOnce(t.Context(), store, settings, now, slog.New(slog.NewTextHandler(io.Discard, nil)), tracenoop.NewTracerProvider().Tracer("test"), service.NewMetrics(metricnoop.NewMeterProvider()))
	require.NoError(t, err)
	items, total, err := store.QueryAuditLogs(state.AuditQuery{})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, items, 2)
	for _, item := range items {
		assert.False(t, item.OccurredAt.Before(now.Add(-24*time.Hour)))
	}
}

func TestPruneAuditRetentionOnceReportsDatabaseFailure(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	require.NoError(t, store.Close())
	err := pruneAuditRetentionOnce(t.Context(), store, model.DefaultRuntimeSettings(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)), tracenoop.NewTracerProvider().Tracer("test"), service.NewMetrics(metricnoop.NewMeterProvider()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestRunAuditRetentionStopsOnCancellation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := runAuditRetention(ctx, store, model.DefaultRuntimeSettings, slog.New(slog.NewTextHandler(io.Discard, nil)), tracenoop.NewTracerProvider().Tracer("test"), service.NewMetrics(metricnoop.NewMeterProvider()))
	require.NoError(t, err)
}

func testConfig(dataDir string) config.Config {
	return config.Config{
		DataDir: dataDir, AdminAddr: "127.0.0.1:0", ObserveAddr: "127.0.0.1:0",
		PollInterval: 30 * time.Second, RequestRate: 10, RequestConcurrency: 2,
		LogLevel: "info", AuditLogRetention: 30 * 24 * time.Hour,
		OTelSDKDisabled: true,
	}
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return listener
}

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func appendAudit(t *testing.T, store *state.Store, occurredAt time.Time, index int) {
	t.Helper()
	_, err := store.AppendAudit(state.AuditLog{OccurredAt: occurredAt, RequestID: fmt.Sprintf("request-%d", index), Action: "test", Outcome: state.AuditSuccess})
	require.NoError(t, err)
}

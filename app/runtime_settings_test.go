package app

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestRuntimeSettingsManagerUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(*model.RuntimeSettings)
		closeDB   bool
		wantError bool
	}{
		{name: "success", mutate: func(settings *model.RuntimeSettings) { settings.PollIntervalSec = 45 }},
		{name: "invalid", mutate: func(settings *model.RuntimeSettings) { settings.DeliveryConcurrency = 0 }, wantError: true},
		{name: "automatic AI without defaults", mutate: func(settings *model.RuntimeSettings) { settings.AIAutoProcessingEnabled = true }, wantError: true},
		{name: "persistence failure", mutate: func(settings *model.RuntimeSettings) { settings.PollIntervalSec = 60 }, closeDB: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := vault.New(bytes.Repeat([]byte{7}, 32))
			require.NoError(t, err)
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), v)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			initial := model.DefaultRuntimeSettings()
			require.NoError(t, store.PutRuntimeSettings(initial))
			events := service.NewEventBus()
			engine := service.NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), service.NewMetrics(metricnoop.NewMeterProvider()), initial, events, nil)
			manager := newRuntimeSettingsManager(store, engine, nil, events)
			updated := initial
			tt.mutate(&updated)
			if tt.closeDB {
				require.NoError(t, store.Close())
			}

			err = manager.UpdateSettings(updated)
			if tt.wantError {
				require.Error(t, err)
				assert.Equal(t, initial, manager.Settings())
				assert.Zero(t, events.Revision())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, updated, manager.Settings())
			stored, err := store.RuntimeSettings()
			require.NoError(t, err)
			assert.Equal(t, updated, stored)
			assert.NotZero(t, events.Revision())
		})
	}
}

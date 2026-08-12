package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/linxin2429/bili_notify/internal/requestgate"
	"github.com/linxin2429/bili_notify/logging"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

// runtimeSettingsManager serializes durable settings updates and publishes a
// new snapshot only after every in-process consumer has accepted it.
type runtimeSettingsManager struct {
	mu           sync.Mutex
	store        *state.Store
	engine       *service.Engine
	loggers      *logging.Set
	events       *service.EventBus
	bilibiliGate *requestgate.Gate
	zsxqGate     *requestgate.Gate
}

func newRuntimeSettingsManager(store *state.Store, engine *service.Engine, loggers *logging.Set, events *service.EventBus, gates ...*requestgate.Gate) *runtimeSettingsManager {
	manager := &runtimeSettingsManager{store: store, engine: engine, loggers: loggers, events: events}
	if len(gates) > 0 {
		manager.bilibiliGate = gates[0]
	}
	if len(gates) > 1 {
		manager.zsxqGate = gates[1]
	}
	return manager
}

func (m *runtimeSettingsManager) Settings() model.RuntimeSettings {
	return m.engine.Settings()
}

func (m *runtimeSettingsManager) UpdateSettings(settings model.RuntimeSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if settings.AIAutoProcessingEnabled {
		if err := m.store.ValidateAutoAIConfiguration(); err != nil {
			return fmt.Errorf("enabling automatic AI processing: %w", err)
		}
	}
	if m.engine.Settings() == settings {
		return nil
	}
	if err := m.store.PutRuntimeSettings(settings); err != nil {
		return fmt.Errorf("persisting runtime settings: %w", err)
	}
	if m.loggers != nil {
		if err := m.loggers.Apply(settings.LogLevel); err != nil {
			return fmt.Errorf("applying logging settings: %w", err)
		}
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if m.bilibiliGate != nil {
		if err := m.bilibiliGate.Update(drainCtx, settings.BilibiliRequestRate, settings.BilibiliRequestConcurrency, 10*time.Second); err != nil {
			return fmt.Errorf("updating Bilibili request gate: %w", err)
		}
	}
	if m.zsxqGate != nil {
		if err := m.zsxqGate.Update(drainCtx, settings.ZSXQRequestRate, settings.ZSXQRequestConcurrency, 10*time.Second); err != nil {
			return fmt.Errorf("updating ZSXQ request gate: %w", err)
		}
	}
	m.engine.ApplySettings(settings)
	m.events.Publish(service.TopicSettings | service.TopicStatus)
	return nil
}

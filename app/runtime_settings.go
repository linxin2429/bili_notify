package app

import (
	"fmt"
	"sync"

	"github.com/linxin2429/bili_notify/logging"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

// runtimeSettingsManager serializes durable settings updates and publishes a
// new snapshot only after every in-process consumer has accepted it.
type runtimeSettingsManager struct {
	mu      sync.Mutex
	store   *state.Store
	engine  *service.Engine
	loggers *logging.Set
	events  *service.EventBus
}

func newRuntimeSettingsManager(store *state.Store, engine *service.Engine, loggers *logging.Set, events *service.EventBus) *runtimeSettingsManager {
	return &runtimeSettingsManager{store: store, engine: engine, loggers: loggers, events: events}
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
	m.engine.ApplySettings(settings)
	m.events.Publish(service.TopicSettings | service.TopicStatus)
	return nil
}

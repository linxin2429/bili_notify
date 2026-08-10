package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRetryDeliveryAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		deliveryState model.DeliveryState
		authenticated bool
		validCSRF     bool
		missing       bool
		wantStatus    int
	}{
		{name: "queues blocked delivery", deliveryState: model.DeliveryBlocked, authenticated: true, validCSRF: true, wantStatus: http.StatusAccepted},
		{name: "rejects pending delivery", deliveryState: model.DeliveryPending, authenticated: true, validCSRF: true, wantStatus: http.StatusConflict},
		{name: "reports missing delivery", authenticated: true, validCSRF: true, missing: true, wantStatus: http.StatusNotFound},
		{name: "requires authentication", deliveryState: model.DeliveryBlocked, wantStatus: http.StatusUnauthorized},
		{name: "requires valid csrf", deliveryState: model.DeliveryBlocked, authenticated: true, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openWebTestStore(t)
			auth, setupCode, err := newAuthenticator(store)
			require.NoError(t, err)
			require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
			events := service.NewEventBus()
			server := &Server{auth: auth, store: store, events: events}
			httpServer := httptest.NewServer(server.adminHandler())
			t.Cleanup(httpServer.Close)

			id := "missing"
			if !tt.missing {
				id = createWebTestDelivery(t, store)
				if tt.deliveryState == model.DeliveryBlocked {
					require.NoError(t, store.FailDelivery(id, true, time.Now().Add(time.Hour), errors.New("blocked"), nil))
				}
			}

			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL+"/api/v3/deliveries/"+id+"/retry", nil)
			require.NoError(t, err)
			if tt.authenticated {
				token, csrf, _, sessionErr := auth.createSession()
				require.NoError(t, sessionErr)
				request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
				if tt.validCSRF {
					request.Header.Set("X-CSRF-Token", csrf)
				} else {
					request.Header.Set("X-CSRF-Token", "invalid")
				}
			}
			response, err := httpServer.Client().Do(request)
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })

			assert.Equal(t, tt.wantStatus, response.StatusCode)
			assert.NotEmpty(t, response.Header.Get("X-Request-ID"))
			assert.Greater(t, events.Revision(), uint64(0))
			auditLogs, total, auditErr := store.QueryAuditLogs(state.AuditQuery{Action: "delivery.retry"})
			require.NoError(t, auditErr)
			require.Equal(t, 1, total)
			require.Len(t, auditLogs, 1)
			assert.Equal(t, tt.wantStatus, auditLogs[0].StatusCode)
			if tt.wantStatus == http.StatusUnauthorized || tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, state.AuditDenied, auditLogs[0].Outcome)
			} else if tt.wantStatus >= 400 {
				assert.Equal(t, state.AuditFailure, auditLogs[0].Outcome)
			} else {
				assert.Equal(t, state.AuditSuccess, auditLogs[0].Outcome)
			}
			if tt.wantStatus == http.StatusAccepted {
				deliveries, listErr := store.ListDeliveries(0)
				require.NoError(t, listErr)
				require.Len(t, deliveries, 1)
				assert.Equal(t, model.DeliveryPending, deliveries[0].State)
				assert.WithinDuration(t, time.Now(), deliveries[0].NextAt, 2*time.Second)
			}
		})
	}
}

func TestChannelAuditDoesNotPersistSecretValues(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	server := &Server{auth: auth, store: store, events: service.NewEventBus()}
	httpServer := httptest.NewServer(server.adminHandler())
	t.Cleanup(httpServer.Close)
	token, csrf, _, err := auth.createSession()
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"name": "robot", "type": "wecom", "enabled": true,
		"settings": map[string]string{}, "secrets": map[string]string{"webhook": "https://secret.example/hook"},
	})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL+"/api/v3/channels", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	response, err := httpServer.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	assert.Equal(t, http.StatusCreated, response.StatusCode)

	entries, total, err := store.QueryAuditLogs(state.AuditQuery{Action: "channel.create"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, entries, 1)
	details, err := json.Marshal(entries[0].Details)
	require.NoError(t, err)
	assert.NotContains(t, string(details), "secret.example")
	assert.Contains(t, string(details), "webhook")
}

func TestLoginAuditSessionCorrelation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		password    string
		wantStatus  int
		wantActor   string
		wantOutcome string
		wantSession bool
	}{
		{name: "successful login records the new session", password: "correct horse battery staple", wantStatus: http.StatusOK, wantActor: "administrator", wantOutcome: state.AuditSuccess, wantSession: true},
		{name: "failed login remains anonymous", password: "wrong password", wantStatus: http.StatusUnauthorized, wantActor: "anonymous", wantOutcome: state.AuditDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openWebTestStore(t)
			auth, setupCode, err := newAuthenticator(store)
			require.NoError(t, err)
			require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
			server := &Server{auth: auth, store: store, events: service.NewEventBus()}
			httpServer := httptest.NewServer(server.adminHandler())
			t.Cleanup(httpServer.Close)

			body, err := json.Marshal(map[string]string{"password": tt.password})
			require.NoError(t, err)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL+"/api/v3/session", bytes.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "audit-test")
			request.Header.Set("X-Forwarded-For", "203.0.113.10")
			response, err := httpServer.Client().Do(request)
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })

			assert.Equal(t, tt.wantStatus, response.StatusCode)
			requestID := response.Header.Get("X-Request-ID")
			assert.NotEmpty(t, requestID)
			entries, total, err := store.QueryAuditLogs(state.AuditQuery{Action: "auth.login"})
			require.NoError(t, err)
			require.Equal(t, 1, total)
			require.Len(t, entries, 1)
			entry := entries[0]
			assert.Equal(t, requestID, entry.RequestID)
			assert.Equal(t, tt.wantActor, entry.Actor)
			assert.Equal(t, tt.wantOutcome, entry.Outcome)
			assert.Equal(t, "audit-test", entry.UserAgent)
			assert.NotEqual(t, "203.0.113.10", entry.RemoteIP)
			if tt.wantSession {
				require.NotEmpty(t, entry.SessionID)
				require.Len(t, response.Cookies(), 1)
				assert.NotEqual(t, response.Cookies()[0].Value, entry.SessionID)
			} else {
				assert.Empty(t, entry.SessionID)
			}
		})
	}
}

func TestAuditWriteFailurePreservesBusinessResponse(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	token, csrf, _, err := auth.createSession()
	require.NoError(t, err)
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	metrics := service.NewMetrics(meterProvider)
	server := &Server{auth: auth, store: store, events: service.NewEventBus(), metrics: metrics}
	httpServer := httptest.NewServer(server.adminHandler())
	t.Cleanup(httpServer.Close)
	require.NoError(t, store.Close())

	request, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, httpServer.URL+"/api/v3/session", nil)
	require.NoError(t, err)
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	response, err := httpServer.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &data))
	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name == "bili_notify.audit.write_failures" {
				sum, ok := measurement.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				require.Len(t, sum.DataPoints, 1)
				assert.Equal(t, int64(1), sum.DataPoints[0].Value)
				return
			}
		}
	}
	t.Fatal("audit write failure metric was not gathered")
}

func createWebTestDelivery(t *testing.T, store *state.Store) string {
	t.Helper()
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true, BaselineReady: true, ExclusiveBaselineReady: true}))
	channel, err := store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	_, err = store.RecordDynamics("42", []model.Dynamic{{ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()}}, []string{channel.ID}, state.DynamicBaselineNone)
	require.NoError(t, err)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	return deliveries[0].ID
}

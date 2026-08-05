package web

import (
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
		wantRevision  bool
	}{
		{name: "queues blocked delivery", deliveryState: model.DeliveryBlocked, authenticated: true, validCSRF: true, wantStatus: http.StatusAccepted, wantRevision: true},
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

			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL+"/api/v1/deliveries/"+id+"/retry", nil)
			require.NoError(t, err)
			if tt.authenticated {
				token, csrf, sessionErr := auth.createSession()
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
			assert.Equal(t, tt.wantRevision, events.Revision() > 0)
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

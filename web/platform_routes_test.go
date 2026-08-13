package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	platformcontract "github.com/linxin2429/bili_notify/platform"
	"github.com/linxin2429/bili_notify/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeletePlatformSessionDispatchesThroughRegisteredModule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		platform model.Platform
	}{
		{name: "Bilibili", platform: model.PlatformBilibili},
		{name: "Knowledge Planet", platform: model.PlatformZSXQ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			called := false
			meta, ok := platformcontract.BuiltinMeta(tt.platform)
			require.True(t, ok)
			events := service.NewEventBus()
			server := &Server{events: events, platformModules: []platformcontract.Module{{Meta: meta,
				Accounts: platformcontract.AccountRoutes{Disconnect: func(context.Context) error { called = true; return nil }}}}}
			request := httptest.NewRequest(http.MethodDelete, "/api/v4/accounts/"+string(tt.platform)+"/session", nil)
			request.SetPathValue("platform", string(tt.platform))
			response := httptest.NewRecorder()

			server.deletePlatformSessionV4(response, request)

			assert.Equal(t, http.StatusNoContent, response.Code)
			assert.True(t, called)
			assert.NotZero(t, events.Revision())
		})
	}
}

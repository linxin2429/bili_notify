package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformAccountLogoutIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		platform model.Platform
	}{
		{name: "bilibili", platform: model.PlatformBilibili},
		{name: "zsxq", platform: model.PlatformZSXQ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, nil)
			require.NoError(t, fixture.store.SaveSession(model.BiliSession{AccountUID: "42", Cookies: map[string]string{"SESSDATA": "bili-secret"}}))
			require.NoError(t, fixture.store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "7", DisplayName: "member", Status: model.AccountConnected, Session: map[string]string{"zsxq_access_token": "planet-secret"}}))
			response := fixture.request(t, http.MethodGet, "/api/v4/accounts", nil, false)
			require.Equal(t, http.StatusOK, response.Code)
			var before []model.PlatformAccount
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &before))
			require.Len(t, before, 2)
			assert.NotContains(t, response.Body.String(), "bili-secret")
			assert.NotContains(t, response.Body.String(), "planet-secret")
			path := "/api/v4/accounts/" + string(tt.platform) + "/session"
			response = fixture.request(t, http.MethodDelete, path, nil, true)
			require.Equal(t, http.StatusNoContent, response.Code)
			response = fixture.request(t, http.MethodDelete, path, nil, true)
			require.Equal(t, http.StatusNoContent, response.Code, "logout is idempotent")
			response = fixture.request(t, http.MethodGet, "/api/v4/accounts", nil, false)
			require.Equal(t, http.StatusOK, response.Code)
			var after []model.PlatformAccount
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &after))
			// Both platforms remain listed; only the selected platform is disconnected.
			require.Len(t, after, 2)
			for _, account := range after {
				if account.Platform == tt.platform {
					assert.NotEqual(t, model.AccountConnected, account.Status)
				} else {
					assert.Equal(t, model.AccountConnected, account.Status)
				}
			}
		})
	}
}

func TestAccountHandlersReportCanceledDatabaseOperations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler func(*Server) http.HandlerFunc
	}{
		{"accounts", func(s *Server) http.HandlerFunc { return s.accountsV4 }},
		{"bilibili logout", func(s *Server) http.HandlerFunc { return s.deleteBilibiliSessionV4 }},
		{"zsxq logout", func(s *Server) http.HandlerFunc { return s.deleteZSXQSessionV4 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, nil)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
			response := httptest.NewRecorder()
			tt.handler(fixture.server)(response, request)
			assertAPIError(t, response, http.StatusInternalServerError, "internal")
		})
	}
}

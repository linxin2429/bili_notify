package zsxq

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncDynamicsArchivesBaselineWithoutChannels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"succeeded":true,"resp_data":{"topics":[{"topic_id":1,"type":"talk","create_time":"2026-08-10T00:00:00Z","talk":{"owner":{"user_id":8,"name":"Owner","role":"owner"},"text":"hello"}}],"end_time":""}}`)
	}))
	t.Cleanup(server.Close)
	key, err := vault.New(bytes.Repeat([]byte{181}, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "user", DisplayName: "User", Status: model.AccountConnected, Session: map[string]string{"access_token": "session"}}
	require.NoError(t, store.PutPlatformAccount(account))
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
		ExternalID: "9", Name: "Planet", OwnerID: "8", Enabled: true, BaselineState: model.BaselinePending}
	require.NoError(t, store.PutSource(source))
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	collector, err := NewCollector(store, client, model.DefaultRuntimeSettings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	require.NoError(t, collector.SyncDynamics(t.Context()))
	contents, err := store.QueryContents(state.PlatformContentQuery{Platform: model.PlatformZSXQ, SourceID: source.ID})
	require.NoError(t, err)
	require.Len(t, contents, 1)
	assert.True(t, contents[0].Baseline)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	updated, err := store.Source(source.ID)
	require.NoError(t, err)
	assert.Equal(t, model.BaselineComplete, updated.BaselineState)
}

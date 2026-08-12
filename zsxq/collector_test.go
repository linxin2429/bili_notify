package zsxq

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/media"
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
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "user", DisplayName: "User", Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: "session"}}
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

func TestSyncDynamicsUsesLatestTokenWithoutChangingSourceAvailability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		enabled      bool
		status       int
		wantRequests int64
		wantError    string
	}{
		{name: "enabled source uses replacement account token", enabled: true, status: http.StatusOK, wantRequests: 1},
		{name: "disabled source remains idle", enabled: false, status: http.StatusOK, wantRequests: 0},
		{name: "permission failure preserves enabled source", enabled: true, status: http.StatusForbidden, wantRequests: 1, wantError: "upstream synchronization failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "new-token", r.Header.Get("Authorization"))
				if tt.status != http.StatusOK {
					w.WriteHeader(tt.status)
					return
				}
				writeEnvelope(t, w, map[string]any{"topics": []any{}, "end_time": ""})
			}))
			t.Cleanup(server.Close)
			key, err := vault.New(bytes.Repeat([]byte{183}, 32))
			require.NoError(t, err)
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), key)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			require.NoError(t, store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "old-user", DisplayName: "Old", Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: "old-token"}}))
			source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
				ExternalID: "9", Name: "Planet", Enabled: tt.enabled, BaselineState: model.BaselineComplete}
			require.NoError(t, store.PutSource(source))
			require.NoError(t, store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "new-user", DisplayName: "New", Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: "new-token"}}))
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			collector, err := NewCollector(store, client, model.DefaultRuntimeSettings, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)

			require.NoError(t, collector.SyncDynamics(t.Context()))
			assert.Equal(t, tt.wantRequests, requests.Load())
			loaded, err := store.Source(source.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.enabled, loaded.Enabled)
			if tt.wantError == "" {
				assert.Empty(t, loaded.LastError)
			} else {
				assert.Contains(t, loaded.LastError, tt.wantError)
			}
		})
	}
}

func TestSyncDynamicsFiltersAuthorsAcrossLivePages(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("end_time") == "" {
			writeEnvelope(t, w, map[string]any{"topics": []any{map[string]any{"topic_id": 3, "type": "talk", "create_time": "2026-08-10T03:00:00Z", "talk": map[string]any{"owner": map[string]any{"user_id": 7, "name": "Other"}, "text": "not selected"}}}, "end_time": "next"})
			return
		}
		writeEnvelope(t, w, map[string]any{"topics": []any{
			map[string]any{"topic_id": 2, "type": "talk", "create_time": "2026-08-10T02:00:00Z", "talk": map[string]any{"owner": map[string]any{"user_id": 8, "name": "Selected"}, "text": "selected"}},
			map[string]any{"topic_id": 1, "type": "talk", "create_time": "2026-08-10T01:00:00Z", "talk": map[string]any{"owner": map[string]any{"user_id": 8, "name": "Selected"}, "text": "already passed watermark"}},
		}, "end_time": ""})
	}))
	t.Cleanup(server.Close)
	key, err := vault.New(bytes.Repeat([]byte{184}, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.PutPlatformAccount(model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "user", Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: "token"}}))
	watermarkContent := model.Content{ID: model.ContentID(model.PlatformZSXQ, "1"), PublishedAt: time.Date(2026, time.August, 10, 1, 0, 0, 0, time.UTC)}
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "9", Name: "Planet", Enabled: true,
		BaselineState: model.BaselineComplete, HighWatermark: encodeWatermark(watermarkContent), ZSXQTopicMode: model.ZSXQTopicSelectedAuthors, ZSXQAuthors: []model.ZSXQAuthor{{UserID: "8", Name: "Selected"}}}
	require.NoError(t, store.PutSource(source))
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	collector, err := NewCollector(store, client, model.DefaultRuntimeSettings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	require.NoError(t, collector.SyncDynamics(t.Context()))
	contents, err := store.QueryContents(state.PlatformContentQuery{Platform: model.PlatformZSXQ, SourceID: source.ID})
	require.NoError(t, err)
	require.Len(t, contents, 1)
	assert.Equal(t, "2", contents[0].ExternalID)
	assert.Equal(t, int64(2), requests.Load())
	updated, err := store.Source(source.ID)
	require.NoError(t, err)
	assert.Equal(t, encodeWatermark(model.Content{ID: model.ContentID(model.PlatformZSXQ, "3"), PublishedAt: time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC)}), updated.HighWatermark)
}

func TestSyncCommentsUsesCompleteTopicPreview(t *testing.T) {
	t.Parallel()
	var commentsEndpointCalled atomic.Bool
	var fileURLCalls atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signed/file" {
			assert.Equal(t, "new-session", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v2/topics/1/comments":
			commentsEndpointCalled.Store(true)
			http.Error(w, "comments endpoint must not be called", http.StatusInternalServerError)
		case "/v2/files/4/download_url":
			fileURLCalls.Add(1)
			writeEnvelope(t, w, map[string]any{"download_url": server.URL + "/signed/file"})
		case "/signed/file":
			_, _ = io.WriteString(w, "pdf")
		default:
			_, _ = io.WriteString(w, `{"succeeded":true,"resp_data":{"topic":{"topic_id":1,"type":"talk","create_time":"2026-08-10T00:00:00Z","comments_count":1,"talk":{"owner":{"user_id":8,"name":"Owner"},"text":"hello","files":[{"file_id":4,"name":"资料.pdf","size":3}]},"show_comments":[{"comment_id":3,"create_time":"2026-08-10T01:00:00Z","owner":{"user_id":8,"name":"Owner"},"text":"answer"}]}}}`)
		}
	}))
	t.Cleanup(server.Close)
	key, err := vault.New(bytes.Repeat([]byte{182}, 32))
	require.NoError(t, err)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "old-user", DisplayName: "Old", Status: model.AccountConnected,
		Session: map[string]string{AccessTokenKey: "old-session"}}
	require.NoError(t, store.PutPlatformAccount(account))
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
		ExternalID: "9", Name: "Planet", OwnerID: "8", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, store.PutSource(source))
	account.ExternalID, account.DisplayName, account.Session = "new-user", "New", map[string]string{AccessTokenKey: "new-session"}
	require.NoError(t, store.PutPlatformAccount(account))
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "1"), Platform: model.PlatformZSXQ, SourceID: source.ID,
		ExternalID: "1", AuthorID: "8", AuthorName: "Owner", UpstreamType: "talk", Type: model.ContentDiscussion,
		PublishedAt: time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)}
	require.NoError(t, store.ArchiveContent(content, nil))
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	collector, err := NewCollector(store, client, model.DefaultRuntimeSettings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	collector.SetAttachmentDownloader(&media.AttachmentDownloader{DataDir: t.TempDir(), Client: server.Client(), AllowPrivateNetwork: true})

	require.NoError(t, collector.SyncComments(t.Context()))
	require.NoError(t, collector.SyncComments(t.Context()))
	tree, incomplete, err := store.CommentTree(content.ID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	_, attachments, err := store.Content(content.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.False(t, commentsEndpointCalled.Load())
	assert.Equal(t, int64(1), fileURLCalls.Load())
	assert.False(t, incomplete)
	assert.True(t, attachments[0].LocalPath != "")
	assert.Equal(t, "answer", tree[0].Message)
	assert.Equal(t, model.RoleOwner, tree[0].Role)
}

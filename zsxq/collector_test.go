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

func TestSyncCommentsUsesCompleteTopicPreview(t *testing.T) {
	t.Parallel()
	var commentsEndpointCalled atomic.Bool
	var fileURLCalls atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: "user", DisplayName: "User", Status: model.AccountConnected,
		Session: map[string]string{AccessTokenKey: "session"}}
	require.NoError(t, store.PutPlatformAccount(account))
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
		ExternalID: "9", Name: "Planet", OwnerID: "8", Enabled: true, BaselineState: model.BaselineComplete}
	require.NoError(t, store.PutSource(source))
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

package media

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachmentDownloaderEnforcesStreamingLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		maxFile    int64
		budget     int64
		wantFailed int
		wantBudget bool
	}{
		{name: "downloads within limits", body: "1234", maxFile: 4, budget: 10},
		{name: "stream exceeds file limit without content length", body: "12345", maxFile: 4, budget: 10, wantFailed: 1},
		{name: "remaining budget is exhausted", body: "12345", maxFile: 10, budget: 4, wantBudget: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Transfer-Encoding", "chunked")
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			downloader := &AttachmentDownloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true}
			attachments := []model.Attachment{{ID: "content:attachment:file", ContentID: "content", ExternalID: "../file", Type: model.AttachmentFile, FileName: "../../bad.txt", RemoteURL: server.URL}}
			result := downloader.EnsureAttachments(t.Context(), model.PlatformZSXQ, "zsxq:planet:9", "zsxq:content:1", attachments, tt.maxFile, tt.budget, nil)
			assert.Equal(t, tt.wantFailed, result.Failed)
			assert.Equal(t, tt.wantBudget, result.BudgetFull)
			if tt.wantFailed == 0 && !tt.wantBudget {
				require.NotEmpty(t, attachments[0].LocalPath)
				assert.NotContains(t, attachments[0].LocalPath, "..")
				info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(attachments[0].LocalPath)))
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}
		})
	}
}

func TestAttachmentDownloaderBilibiliImagePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       []byte
		wantFailed int
	}{
		{name: "recognized PNG", body: testPNG},
		{name: "HTML rejected", body: []byte("<html>not an image</html>"), wantFailed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "https://www.bilibili.com/", request.Header.Get("Referer"))
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(server.Close)
			attachments := []model.Attachment{{ID: "image", ContentID: "content", ExternalID: "image", Type: model.AttachmentImage, RemoteURL: server.URL}}
			auth := AuthorizeMediaFunc(func(request *http.Request) { request.Header.Set("Referer", "https://www.bilibili.com/") })
			result := (&AttachmentDownloader{DataDir: t.TempDir(), Client: server.Client(), AllowPrivateNetwork: true}).
				EnsureAttachments(t.Context(), model.PlatformBilibili, "bilibili:up:42", "bilibili:content:1", attachments, 1<<20, 1<<20, auth)
			assert.Equal(t, tt.wantFailed, result.Failed)
			if tt.wantFailed == 0 {
				assert.Equal(t, 1, result.Downloaded)
				assert.NotEmpty(t, attachments[0].LocalPath)
			}
		})
	}
}

func TestAttachmentRedirectNeverForwardsZSXQSession(t *testing.T) {
	t.Parallel()
	var originCookie string
	var originUserAgent string
	var redirectedCookie string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte("asset"))
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCookie = r.Header.Get("Cookie")
		originUserAgent = r.Header.Get("User-Agent")
		http.Redirect(w, &http.Request{}, target.URL+"/download", http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	originAddress := strings.TrimPrefix(origin.URL, "http://")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "api.zsxq.com:80" {
			address = originAddress
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	downloader := &AttachmentDownloader{DataDir: t.TempDir(), Client: &http.Client{Transport: transport}, AllowPrivateNetwork: true}
	attachments := []model.Attachment{{ID: "a", ContentID: "c", ExternalID: "a", Type: model.AttachmentFile, RemoteURL: "http://api.zsxq.com/attachment"}}
	auth := AuthorizeMediaFunc(func(request *http.Request) {
		request.Header.Set("User-Agent", "zsxq-browser")
		request.AddCookie(&http.Cookie{Name: "zsxq_access_token", Value: "secret"})
	})
	result := downloader.EnsureAttachments(t.Context(), model.PlatformZSXQ, "source", "content", attachments, 100, 100, auth)
	require.Equal(t, 1, result.Downloaded)
	assert.Equal(t, "zsxq_access_token=secret", originCookie)
	assert.Equal(t, "zsxq-browser", originUserAgent)
	assert.Empty(t, redirectedCookie)
}

func TestAttachmentDownloaderRejectsSymlinkMediaTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "media", "zsxq"), 0o700))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "media", "zsxq", "source")))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("asset")) }))
	t.Cleanup(server.Close)
	downloader := &AttachmentDownloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true}
	attachments := []model.Attachment{{ID: "a", ContentID: "c", ExternalID: "a", Type: model.AttachmentFile, RemoteURL: server.URL}}
	result := downloader.EnsureAttachments(t.Context(), model.PlatformZSXQ, "source", "content", attachments, 100, 100, nil)
	assert.Equal(t, 1, result.Failed)
	assert.True(t, strings.Contains(attachments[0].LocalizeError, "failed"))
}

func TestAttachmentDownloaderPersistsConflictSafeFinalPath(t *testing.T) {
	t.Parallel()
	var body = "first"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	downloader := &AttachmentDownloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true}
	first := []model.Attachment{{ID: "a", ContentID: "content", ExternalID: "asset", Type: model.AttachmentFile, FileName: "report.txt", RemoteURL: server.URL}}
	result := downloader.EnsureAttachments(t.Context(), model.PlatformZSXQ, "source", "content", first, 100, 1000, nil)
	require.Equal(t, 1, result.Downloaded)
	body = "second"
	second := []model.Attachment{{ID: "b", ContentID: "content", ExternalID: "asset", Type: model.AttachmentFile, FileName: "report.txt", RemoteURL: server.URL}}
	result = downloader.EnsureAttachments(t.Context(), model.PlatformZSXQ, "source", "content", second, 100, 1000, nil)
	require.Equal(t, 1, result.Downloaded)
	assert.NotEqual(t, first[0].LocalPath, second[0].LocalPath)
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(first[0].LocalPath)))
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(second[0].LocalPath)))
}

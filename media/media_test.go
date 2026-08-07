package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEnsureDownloadsAndSkipsExisting(t *testing.T) {
	t.Parallel()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		assert.Equal(t, "https://www.bilibili.com/", r.Header.Get("Referer"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	d := &Downloader{DataDir: dir, Client: server.Client(), UserAgent: "test-agent"}
	dynamic := &model.Dynamic{
		ID: "100", UID: "42",
		Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: server.URL + "/a.png"}},
		Original: &model.Dynamic{
			ID: "99", UID: "7",
			Media: []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: server.URL + "/b.png"}},
		},
	}
	ok, bad, _ := d.Ensure(t.Context(), dynamic)
	require.Equal(t, 0, bad)
	require.Equal(t, 2, ok)
	require.NotEmpty(t, dynamic.Media[0].LocalPath)
	require.NotEmpty(t, dynamic.Original.Media[0].LocalPath)
	assert.Equal(t, "image/png", dynamic.Media[0].ContentType)
	assert.Equal(t, int64(len(png)), dynamic.Media[0].Size)
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(dynamic.Media[0].LocalPath)))
	assert.Equal(t, 2, hits)

	ok, bad, _ = d.Ensure(t.Context(), dynamic)
	require.Equal(t, 0, bad)
	require.Equal(t, 0, ok)
	assert.Equal(t, 2, hits)
}

func TestEnsureFailureLeavesURL(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	d := &Downloader{DataDir: dir, Client: server.Client()}
	dynamic := &model.Dynamic{
		ID: "1", UID: "2",
		Media: []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: server.URL + "/missing.jpg"}},
	}
	ok, bad, _ := d.Ensure(t.Context(), dynamic)
	assert.Equal(t, 0, ok)
	assert.Equal(t, 1, bad)
	assert.Empty(t, dynamic.Media[0].LocalPath)
	assert.Equal(t, server.URL+"/missing.jpg", dynamic.Media[0].URL)
}

func TestEnsureRejectsTooLarge(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(make([]byte, MaxFileSize+1))
	}))
	t.Cleanup(server.Close)
	d := &Downloader{DataDir: t.TempDir(), Client: server.Client()}
	dynamic := &model.Dynamic{
		ID: "1", UID: "2",
		Media: []model.DynamicMedia{{URL: server.URL + "/big.jpg"}},
	}
	ok, bad, _ := d.Ensure(t.Context(), dynamic)
	assert.Equal(t, 0, ok)
	assert.Equal(t, 1, bad)
	assert.Empty(t, dynamic.Media[0].LocalPath)
}

func TestDownloadTraceDoesNotRecordURLOrQuery(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("image-data"))
	}))
	t.Cleanup(server.Close)
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })

	d := &Downloader{
		DataDir: t.TempDir(),
		Client:  server.Client(),
		Tracer:  tracerProvider.Tracer("test/media"),
	}
	dynamic := &model.Dynamic{
		ID: "1", UID: "2",
		Media: []model.DynamicMedia{{URL: server.URL + "/image.png?token=secret-query-value"}},
	}
	downloaded, failed, _ := d.Ensure(t.Context(), dynamic)
	require.Equal(t, 1, downloaded)
	require.Equal(t, 0, failed)

	spans := spanRecorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "media.download", spans[0].Name())
	for _, value := range spans[0].Attributes() {
		assert.NotEqual(t, "url.full", string(value.Key))
		assert.NotEqual(t, "url.query", string(value.Key))
		assert.False(t, strings.Contains(value.Value.Emit(), "secret-query-value"))
	}
}

func TestResolveAndRemoveUP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{name: "valid", rel: "media/1/2/0.jpg"},
		{name: "escape", rel: "media/../secret", wantErr: true},
		{name: "outside", rel: "other/1.jpg", wantErr: true},
		{name: "empty", rel: "", wantErr: true},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			abs, err := Resolve(dir, tt.rel)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(abs, filepath.Clean(dir)))
		})
	}

	path := filepath.Join(dir, "media", "42", "9", "0.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	require.NoError(t, RemoveUP(dir, "42"))
	_, err := os.Stat(filepath.Join(dir, "media", "42"))
	assert.True(t, os.IsNotExist(err))
}

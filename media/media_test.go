package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde,
}

type mediaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mediaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type partialMediaBody struct {
	sent bool
}

func (b *partialMediaBody) Read(buffer []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(buffer, testPNG), nil
}

func (b *partialMediaBody) Close() error { return nil }

func TestEnsureDownloadsAndSkipsExisting(t *testing.T) {
	t.Parallel()
	png := testPNG
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		assert.Equal(t, "https://www.bilibili.com/", r.Header.Get("Referer"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	d := &Downloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true, UserAgent: "test-agent"}
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
	d := &Downloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true}
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
	d := &Downloader{DataDir: t.TempDir(), Client: server.Client(), AllowPrivateNetwork: true}
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
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(server.Close)
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })

	d := &Downloader{
		DataDir:             t.TempDir(),
		Client:              server.Client(),
		AllowPrivateNetwork: true,
		Tracer:              tracerProvider.Tracer("test/media"),
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

func TestEnsureRedirectValidationCancellationAndCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		handler    http.Handler
		prepare    func(*testing.T, string)
		context    func(*testing.T) context.Context
		wantOK     int
		wantFailed int
	}{
		{
			name: "redirect to image",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/image", http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNG)
			}),
			wantOK: 1,
		},
		{
			name: "non image response",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = io.WriteString(w, "<html>not an image</html>")
			}),
			wantFailed: 1,
		},
		{
			name: "canceled request",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			}),
			context: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			wantFailed: 1,
		},
		{
			name: "rename failure removes temporary file",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNG)
			}),
			prepare: func(t *testing.T, dir string) {
				rel := relativePath("2", "1", 0, ".png")
				require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o700))
			},
			wantFailed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			t.Cleanup(server.Close)
			dir := t.TempDir()
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}
			ctx := t.Context()
			if tt.context != nil {
				ctx = tt.context(t)
			}
			dynamic := &model.Dynamic{ID: "1", UID: "2", Media: []model.DynamicMedia{{URL: server.URL + "/start"}}}
			ok, failed, _ := (&Downloader{DataDir: dir, Client: server.Client(), AllowPrivateNetwork: true}).Ensure(ctx, dynamic)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantFailed, failed)
			matches, err := filepath.Glob(filepath.Join(dir, "media", "2", "1", ".media-*"))
			require.NoError(t, err)
			assert.Empty(t, matches)
		})
	}
}

func TestPartialResponseFailureCreatesNoMediaOrTemporaryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := &http.Client{Transport: mediaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       &partialMediaBody{},
		}, nil
	})}
	dynamic := &model.Dynamic{ID: "1", UID: "2", Media: []model.DynamicMedia{{URL: "https://media.example/image.png"}}}
	downloaded, failed, downloadedBytes := (&Downloader{DataDir: dir, Client: client, AllowPrivateNetwork: true}).Ensure(t.Context(), dynamic)
	assert.Zero(t, downloaded)
	assert.Equal(t, 1, failed)
	assert.Zero(t, downloadedBytes)
	assert.Empty(t, dynamic.Media[0].LocalPath)
	_, err := os.Stat(filepath.Join(dir, "media"))
	assert.True(t, os.IsNotExist(err))
}

func TestMediaFilesystemRejectsSymbolicLinkEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, string, string) error
	}{
		{
			name: "read through media directory link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.png"), testPNG, 0o600))
				require.NoError(t, os.Symlink(outside, filepath.Join(dataDir, "media")))
				_, _, err := ReadFile(dataDir, "media/secret.png")
				return err
			},
		},
		{
			name: "read through file link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "media", "1"), 0o700))
				secret := filepath.Join(outside, "secret.png")
				require.NoError(t, os.WriteFile(secret, testPNG, 0o600))
				require.NoError(t, os.Symlink(secret, filepath.Join(dataDir, "media", "1", "0.png")))
				_, _, err := ReadFile(dataDir, "media/1/0.png")
				return err
			},
		},
		{
			name: "remove through parent link",
			run: func(t *testing.T, dataDir, outside string) error {
				require.NoError(t, os.MkdirAll(filepath.Join(outside, "42"), 0o700))
				require.NoError(t, os.Symlink(outside, filepath.Join(dataDir, "media")))
				return RemoveUP(dataDir, "42")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			outside := t.TempDir()
			err := tt.run(t, dataDir, outside)
			require.Error(t, err)
			assert.True(t, errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "symbolic link"), err.Error())
			assert.DirExists(t, outside)
		})
	}
}

func TestRemoteMediaURLSecurityPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rawURL       string
		allowPrivate bool
		wantErr      bool
	}{
		{name: "loopback IPv4", rawURL: "http://127.0.0.1/image.png", wantErr: true},
		{name: "loopback IPv6", rawURL: "http://[::1]/image.png", wantErr: true},
		{name: "private address", rawURL: "http://10.0.0.1/image.png", wantErr: true},
		{name: "link local address", rawURL: "http://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "unsupported scheme", rawURL: "file:///etc/passwd", wantErr: true},
		{name: "explicit private-network client", rawURL: "http://127.0.0.1/image.png", allowPrivate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target, err := url.Parse(tt.rawURL)
			require.NoError(t, err)
			err = validateRemoteURL(t.Context(), target, tt.allowPrivate)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
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

	path := filepath.Join(dir, filepath.FromSlash(relativePath("42", "9", 0, ".jpg")))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	require.NoError(t, RemoveUP(dir, "42"))
	_, err := os.Stat(filepath.Dir(filepath.Dir(path)))
	assert.True(t, os.IsNotExist(err))
}

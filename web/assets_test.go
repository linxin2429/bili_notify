package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashedAssetsAreCachedAndCompressed(t *testing.T) {
	t.Parallel()
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	handler := hashedAssets(http.FileServer(http.FS(static)))
	jsPath := firstEmbeddedAsset(t, ".js")
	fontPath := firstEmbeddedAsset(t, ".woff2")

	tests := []struct {
		name            string
		path            string
		acceptEncoding  string
		wantEncoding    string
		wantCache       string
		wantContentType string
		gunzip          bool
	}{
		{
			name:            "javascript accepts gzip",
			path:            jsPath,
			acceptEncoding:  "gzip, deflate",
			wantEncoding:    "gzip",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
			gunzip:          true,
		},
		{
			name:            "javascript without gzip",
			path:            jsPath,
			acceptEncoding:  "",
			wantEncoding:    "",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
		},
		{
			name:           "woff2 stays uncompressed",
			path:           fontPath,
			acceptEncoding: "gzip",
			wantEncoding:   "",
			wantCache:      hashedAssetCache,
		},
		{
			name:            "uppercase gzip token",
			path:            jsPath,
			acceptEncoding:  "GZIP",
			wantEncoding:    "gzip",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
			gunzip:          true,
		},
		{
			name:            "positive quality",
			path:            jsPath,
			acceptEncoding:  "gzip;q=0.8",
			wantEncoding:    "gzip",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
			gunzip:          true,
		},
		{
			name:            "explicit zero quality",
			path:            jsPath,
			acceptEncoding:  "gzip;q=0",
			wantEncoding:    "",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
		},
		{
			name:            "unrelated substring",
			path:            jsPath,
			acceptEncoding:  "x-gzip-test",
			wantEncoding:    "",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
		},
		{
			name:            "wildcard accepts gzip",
			path:            jsPath,
			acceptEncoding:  "*",
			wantEncoding:    "gzip",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
			gunzip:          true,
		},
		{
			name:            "explicit gzip refusal beats wildcard",
			path:            jsPath,
			acceptEncoding:  "gzip;q=0, *;q=1",
			wantEncoding:    "",
			wantCache:       hashedAssetCache,
			wantContentType: "text/javascript",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.acceptEncoding != "" {
				request.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, tt.wantCache, response.Header().Get("Cache-Control"))
			assert.Equal(t, "Accept-Encoding", response.Header().Get("Vary"))
			assert.Equal(t, tt.wantEncoding, response.Header().Get("Content-Encoding"))
			if tt.wantContentType != "" {
				assert.Contains(t, response.Header().Get("Content-Type"), tt.wantContentType)
			}
			body := response.Body.Bytes()
			require.NotEmpty(t, body)
			if !tt.gunzip {
				return
			}
			reader, err := gzip.NewReader(bytes.NewReader(body))
			require.NoError(t, err)
			t.Cleanup(func() { _ = reader.Close() })
			decoded, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.NotEmpty(t, decoded)
			assert.Less(t, len(body), len(decoded))
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "empty", header: "", want: false},
		{name: "deflate only", header: "deflate", want: false},
		{name: "standard list", header: "gzip, deflate", want: true},
		{name: "uppercase", header: "GZIP", want: true},
		{name: "zero quality", header: "gzip;q=0", want: false},
		{name: "zero point zero", header: "gzip;q=0.0", want: false},
		{name: "positive quality", header: "gzip;q=0.5", want: true},
		{name: "substring token", header: "x-gzip-test", want: false},
		{name: "wildcard", header: "*", want: true},
		{name: "wildcard refused", header: "*;q=0", want: false},
		{name: "explicit refuse overrides star", header: "gzip;q=0, *;q=1", want: false},
		{name: "invalid quality", header: "gzip;q=2", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, acceptsGzip(tt.header))
		})
	}
}

func TestHashedAssetErrorsAreNotImmutable(t *testing.T) {
	t.Parallel()
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	handler := hashedAssets(http.FileServer(http.FS(static)))
	request := httptest.NewRequest(http.MethodGet, "/assets/missing-not-present.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
	assert.Equal(t, hashedAssetErrorCache, response.Header().Get("Cache-Control"))
	assert.Empty(t, response.Header().Get("Content-Encoding"))
}

func TestHashedAssetsSkipGzipOnRange(t *testing.T) {
	t.Parallel()
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	handler := hashedAssets(http.FileServer(http.FS(static)))
	jsPath := firstEmbeddedAsset(t, ".js")

	full := httptest.NewRequest(http.MethodGet, jsPath, nil)
	fullResponse := httptest.NewRecorder()
	handler.ServeHTTP(fullResponse, full)
	require.Equal(t, http.StatusOK, fullResponse.Code)
	identity := fullResponse.Body.Bytes()
	require.Greater(t, len(identity), 10)

	request := httptest.NewRequest(http.MethodGet, jsPath, nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Range", "bytes=0-10")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusPartialContent, response.Code)
	assert.Empty(t, response.Header().Get("Content-Encoding"))
	assert.Contains(t, response.Header().Get("Content-Range"), "bytes 0-10/")
	assert.Equal(t, identity[:11], response.Body.Bytes())
	assert.Equal(t, hashedAssetCache, response.Header().Get("Cache-Control"))
}

func TestIndexStaysUncachedWhenAssetsAreImmutable(t *testing.T) {
	t.Parallel()
	static, err := fs.Sub(assets, "dist")
	require.NoError(t, err)
	server := &Server{static: static}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	server.adminHandler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "no-cache", response.Header().Get("Cache-Control"))
	assert.Empty(t, response.Header().Get("Content-Encoding"))
	assert.Contains(t, response.Body.String(), `<div id="root"></div>`)
}

func firstEmbeddedAsset(t *testing.T, ext string) string {
	t.Helper()
	var name string
	err := fs.WalkDir(assets, "dist/assets", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ext) {
			return nil
		}
		name = "/" + strings.TrimPrefix(path, "dist/")
		return fs.SkipAll
	})
	require.NoError(t, err)
	require.NotEmpty(t, name, "embedded dist/assets must contain %s (run frontend-build)", ext)
	return name
}

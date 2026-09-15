package web

import (
	"compress/gzip"
	"net/http"
	"path"
	"strings"
)

const hashedAssetCache = "public, max-age=31536000, immutable"

func hashedAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", hashedAssetCache)
		w.Header().Set("Vary", "Accept-Encoding")
		if !compressHashedAsset(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(&gzipAssetWriter{ResponseWriter: w, gz: gz}, r)
	})
}

func compressHashedAsset(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	switch strings.ToLower(path.Ext(r.URL.Path)) {
	case ".js", ".css", ".svg", ".json", ".map", ".txt", ".html", ".wasm":
		return true
	default:
		return false
	}
}

type gzipAssetWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipAssetWriter) Write(p []byte) (int, error) {
	w.Header().Del("Content-Length")
	return w.gz.Write(p)
}

func (w *gzipAssetWriter) WriteHeader(status int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipAssetWriter) Flush() {
	_ = w.gz.Flush()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

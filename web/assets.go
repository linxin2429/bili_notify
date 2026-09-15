package web

import (
	"compress/gzip"
	"net/http"
	"path"
	"strconv"
	"strings"
)

const (
	hashedAssetCache      = "public, max-age=31536000, immutable"
	hashedAssetErrorCache = "no-store"
)

func hashedAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		writer := &hashedAssetWriter{ResponseWriter: w, compress: compressHashedAsset(r)}
		next.ServeHTTP(writer, r)
		writer.close()
	})
}

func compressHashedAsset(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Range")) != "" {
		return false
	}
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return false
	}
	switch strings.ToLower(path.Ext(r.URL.Path)) {
	case ".js", ".css", ".svg", ".json", ".map", ".txt", ".html", ".wasm":
		return true
	default:
		return false
	}
}

func acceptsGzip(header string) bool {
	gzipQ := -1.0
	starQ := -1.0
	for _, part := range strings.Split(header, ",") {
		name, quality, ok := parseEncoding(part)
		if !ok {
			continue
		}
		switch name {
		case "gzip":
			gzipQ = quality
		case "*":
			starQ = quality
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

func parseEncoding(part string) (string, float64, bool) {
	part = strings.TrimSpace(part)
	if part == "" {
		return "", 0, false
	}
	name, rest, _ := strings.Cut(part, ";")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", 0, false
	}
	quality := 1.0
	for _, param := range strings.Split(rest, ";") {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		key, value, cut := strings.Cut(param, "=")
		if !cut || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || parsed < 0 || parsed > 1 {
			return name, 0, true
		}
		quality = parsed
	}
	return name, quality, true
}

type hashedAssetWriter struct {
	http.ResponseWriter
	compress      bool
	gz            *gzip.Writer
	headerWritten bool
}

func (w *hashedAssetWriter) WriteHeader(status int) {
	if w.headerWritten {
		return
	}
	w.headerWritten = true
	if status < 400 {
		w.Header().Set("Cache-Control", hashedAssetCache)
	} else {
		w.Header().Set("Cache-Control", hashedAssetErrorCache)
	}
	if w.compress && status < 400 {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *hashedAssetWriter) Write(p []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *hashedAssetWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *hashedAssetWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
	}
}

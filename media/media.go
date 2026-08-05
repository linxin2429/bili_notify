// Package media downloads and stores dynamic pictures/covers under data_dir/media.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/linxin2429/bili_notify/model"
)

const (
	// MaxFileSize is the hard download limit per media item.
	MaxFileSize int64 = 10 << 20
	// WeComMaxImageSize is the WeCom robot image payload limit (raw bytes).
	WeComMaxImageSize int64 = 2 << 20
)

// ErrTooLarge is returned when a remote object exceeds MaxFileSize.
var ErrTooLarge = errors.New("media exceeds size limit")

// Downloader fetches CDN media into dataDir/media/{uid}/{dynamic_id}/{index}{ext}.
type Downloader struct {
	DataDir   string
	Client    *http.Client
	UserAgent string
}

// Ensure downloads missing media for d and its Original chain. Failures leave
// LocalPath empty and never return a fatal error to the caller — collection must proceed.
func (d *Downloader) Ensure(ctx context.Context, dynamic *model.Dynamic) (downloaded int, failed int) {
	if d == nil || dynamic == nil {
		return 0, 0
	}
	for current := dynamic; current != nil; current = current.Original {
		ok, bad := d.ensureOne(ctx, current)
		downloaded += ok
		failed += bad
	}
	return downloaded, failed
}

func (d *Downloader) ensureOne(ctx context.Context, dynamic *model.Dynamic) (downloaded int, failed int) {
	if dynamic.ID == "" || dynamic.UID == "" || dynamic.UID == "system" {
		return 0, 0
	}
	for i := range dynamic.Media {
		item := &dynamic.Media[i]
		if item.LocalPath != "" {
			if abs, err := Resolve(d.DataDir, item.LocalPath); err == nil {
				if st, err := os.Stat(abs); err == nil && st.Size() > 0 {
					continue
				}
			}
			item.LocalPath = ""
			item.ContentType = ""
			item.Size = 0
		}
		if strings.TrimSpace(item.URL) == "" {
			failed++
			continue
		}
		if err := d.download(ctx, dynamic.UID, dynamic.ID, i, item); err != nil {
			failed++
			continue
		}
		downloaded++
	}
	return downloaded, failed
}

func (d *Downloader) download(ctx context.Context, uid, dynamicID string, index int, item *model.DynamicMedia) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return err
	}
	ua := d.UserAgent
	if ua == "" {
		ua = "bili-notify"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxFileSize {
		return ErrTooLarge
	}

	limited := io.LimitReader(resp.Body, MaxFileSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(data)) > MaxFileSize {
		return ErrTooLarge
	}
	if len(data) == 0 {
		return errors.New("empty media body")
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}
	ext := extensionFor(contentType, data)

	rel := relativePath(uid, dynamicID, index, ext)
	abs, err := Resolve(d.DataDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".media-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return err
	}
	ok = true
	item.LocalPath = rel
	item.ContentType = contentType
	item.Size = int64(len(data))
	return nil
}

func relativePath(uid, dynamicID string, index int, ext string) string {
	return filepath.ToSlash(filepath.Join("media", uid, dynamicID, strconv.Itoa(index)+ext))
}

// Resolve joins dataDir with a relative media path and rejects path escape.
func Resolve(dataDir, rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || strings.Contains(rel, "..") || !strings.HasPrefix(rel, "media/") {
		return "", errors.New("invalid media path")
	}
	abs := filepath.Join(dataDir, filepath.FromSlash(rel))
	root := filepath.Clean(filepath.Join(dataDir, "media")) + string(os.PathSeparator)
	clean := filepath.Clean(abs)
	if clean != filepath.Clean(filepath.Join(dataDir, "media")) && !strings.HasPrefix(clean+string(os.PathSeparator), root) {
		// allow the file itself under media/
		if !strings.HasPrefix(clean, root) {
			return "", errors.New("media path escapes data directory")
		}
	}
	return clean, nil
}

// RemoveUP deletes all on-disk media for one UP. Missing directory is fine.
func RemoveUP(dataDir, uid string) error {
	uid = strings.TrimSpace(uid)
	if uid == "" || strings.Contains(uid, "..") || strings.ContainsAny(uid, `/\`) {
		return errors.New("invalid uid")
	}
	path := filepath.Join(dataDir, "media", uid)
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing media for uid %s: %w", uid, err)
	}
	return nil
}

// ReadFile resolves and reads a local media file.
func ReadFile(dataDir, rel string) (data []byte, contentType string, err error) {
	abs, err := Resolve(dataDir, rel)
	if err != nil {
		return nil, "", err
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		return nil, "", err
	}
	contentType = http.DetectContentType(data)
	return data, contentType, nil
}

func extensionFor(contentType string, data []byte) string {
	switch strings.ToLower(contentType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	detected := http.DetectContentType(data)
	switch detected {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

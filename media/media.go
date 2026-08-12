// Package media downloads and stores dynamic pictures/covers under data_dir/media.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/linxin2429/bili_notify/model"
	"github.com/spf13/fileflow"
	"github.com/spf13/pathologize"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	// MaxFileSize is the hard download limit per media item.
	MaxFileSize int64 = 10 << 20
	// WeComMaxImageSize is the WeCom robot image payload limit (raw bytes).
	WeComMaxImageSize int64 = 2 << 20
)

// ErrTooLarge is returned when a remote object exceeds MaxFileSize.
var ErrTooLarge = errors.New("media exceeds size limit")

// Downloader fetches Bilibili CDN media into the platform/source/content tree.
type Downloader struct {
	DataDir   string
	Client    *http.Client
	UserAgent string
	Tracer    trace.Tracer
	// AllowPrivateNetwork is intended for explicitly trusted test/private
	// deployments. Production downloads reject loopback and private targets.
	AllowPrivateNetwork bool
}

// Ensure downloads missing media for d and its Original chain. Failures leave
// LocalPath empty and never return a fatal error to the caller — collection must proceed.
func (d *Downloader) Ensure(ctx context.Context, dynamic *model.Dynamic) (downloaded int, failed int, downloadedBytes int64) {
	if d == nil || dynamic == nil {
		return 0, 0, 0
	}
	for current := dynamic; current != nil; current = current.Original {
		ok, bad, size := d.ensureOne(ctx, current)
		downloaded += ok
		failed += bad
		downloadedBytes += size
	}
	return downloaded, failed, downloadedBytes
}

func (d *Downloader) ensureOne(ctx context.Context, dynamic *model.Dynamic) (downloaded int, failed int, downloadedBytes int64) {
	if dynamic.ID == "" || dynamic.UID == "" || dynamic.UID == "system" {
		return 0, 0, 0
	}
	for i := range dynamic.Media {
		item := &dynamic.Media[i]
		if item.LocalPath != "" {
			if abs, err := Resolve(d.DataDir, item.LocalPath); err == nil {
				if st, err := os.Stat(abs); err == nil && st.Size() > 0 && rejectSymlinkPath(d.DataDir, abs, false) == nil {
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
		downloadedBytes += item.Size
	}
	return downloaded, failed, downloadedBytes
}

func (d *Downloader) download(ctx context.Context, uid, dynamicID string, index int, item *model.DynamicMedia) (err error) {
	ctx, span := d.tracer().Start(ctx, "media.download", trace.WithSpanKind(trace.SpanKindClient))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "media download failed")
		} else {
			span.SetAttributes(attribute.String("result", "success"))
		}
		span.End()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errors.New("media URL must use HTTP or HTTPS")
	}
	if err := validateRemoteURL(ctx, req.URL, d.AllowPrivateNetwork); err != nil {
		return err
	}
	ua := d.UserAgent
	if ua == "" {
		ua = "bili-notify"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	client := d.redirectSafeClient(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
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

	detectedType := http.DetectContentType(data)
	if !strings.HasPrefix(strings.ToLower(detectedType), "image/") {
		return errors.New("media response is not an image")
	}
	contentType := detectedType
	ext := extensionFor(contentType, data)

	rel := relativePath(uid, dynamicID, index, ext)
	abs, err := Resolve(d.DataDir, rel)
	if err != nil {
		return err
	}
	if err := rejectSymlinkPath(d.DataDir, filepath.Dir(abs), true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.DataDir, ".media-*")
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
	finalRelative, err := moveIntoMedia(d.DataDir, tmpName, abs)
	if err != nil {
		return err
	}
	ok = true
	item.LocalPath = finalRelative
	item.ContentType = contentType
	item.Size = int64(len(data))
	return nil
}

func (d *Downloader) redirectSafeClient(ctx context.Context) *http.Client {
	base := d.Client
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	if !d.AllowPrivateNetwork {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if configured, ok := base.Transport.(*http.Transport); ok {
			transport = configured.Clone()
		}
		// Resolve and dial the exact validated address. A separate preflight
		// lookup is insufficient because DNS can change between validation and
		// connection establishment. Media downloads also bypass environment
		// proxies so the destination policy cannot be bypassed by proxy routing.
		transport.Proxy = nil
		transport.DialTLSContext = nil
		transport.DialContext = securePublicDial
		client.Transport = transport
	}
	previous := base.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return validateRemoteURL(ctx, request.URL, d.AllowPrivateNetwork)
	}
	return &client
}

func securePublicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parsing media destination: %w", err)
	}
	addresses, err := publicAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	var dialErr error
	for _, candidate := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return connection, nil
		}
		dialErr = errors.Join(dialErr, err)
	}
	return nil, fmt.Errorf("connecting to media host: %w", dialErr)
}

func validateRemoteURL(ctx context.Context, target *url.URL, allowPrivate bool) error {
	if target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return errors.New("media URL must use HTTP or HTTPS with a host")
	}
	if allowPrivate {
		return nil
	}
	_, err := publicAddresses(ctx, target.Hostname())
	return err
}

func publicAddresses(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolving media host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("media host has no IP addresses")
	}
	for _, address := range addresses {
		if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() {
			return nil, errors.New("media URL resolves to a non-public address")
		}
	}
	return addresses, nil
}

func (d *Downloader) tracer() trace.Tracer {
	if d.Tracer != nil {
		return d.Tracer
	}
	return noop.NewTracerProvider().Tracer("github.com/linxin2429/bili_notify/media")
}

func relativePath(uid, dynamicID string, index int, ext string) string {
	return filepath.ToSlash(pathologize.Join("media", string(model.PlatformBilibili),
		model.SourceID(model.PlatformBilibili, uid),
		model.ContentID(model.PlatformBilibili, dynamicID), strconv.Itoa(index)+ext))
}

func moveIntoMedia(dataDir, temporaryPath, destination string) (string, error) {
	flow := fileflow.Flow{DirMode: 0o700}
	finalPath, err := flow.Move(temporaryPath, destination)
	if err != nil {
		return "", fmt.Errorf("committing media file: %w", err)
	}
	if err := rejectSymlinkPath(dataDir, finalPath, false); err != nil {
		_ = os.Remove(finalPath)
		return "", err
	}
	relative, err := filepath.Rel(dataDir, finalPath)
	if err != nil {
		_ = os.Remove(finalPath)
		return "", fmt.Errorf("resolving stored media path: %w", err)
	}
	relative = filepath.ToSlash(relative)
	if _, err := Resolve(dataDir, relative); err != nil {
		_ = os.Remove(finalPath)
		return "", err
	}
	return relative, nil
}

// Resolve joins dataDir with a relative media path and rejects path escape.
func Resolve(dataDir, rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(filepath.FromSlash(rel)) != "" {
		return "", errors.New("invalid media path")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(rel))
	parts := strings.Split(cleanRelative, string(os.PathSeparator))
	if len(parts) < 2 || parts[0] != "media" {
		return "", errors.New("invalid media path")
	}
	for _, part := range parts {
		if part == ".." || part == "." || part == "" {
			return "", errors.New("invalid media path")
		}
	}
	root := filepath.Clean(filepath.Join(dataDir, "media"))
	clean := filepath.Clean(filepath.Join(dataDir, cleanRelative))
	underRoot, err := filepath.Rel(root, clean)
	if err != nil || underRoot == ".." || strings.HasPrefix(underRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("media path escapes data directory")
	}
	return clean, nil
}

// rejectSymlinkPath prevents an attacker-controlled link below dataDir from
// redirecting media reads, writes, or deletion outside the data directory.
func rejectSymlinkPath(dataDir, target string, allowMissing bool) error {
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolving data directory: %w", err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolving media path: %w", err)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("media path escapes data directory")
	}
	current := base
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if allowMissing && os.IsNotExist(statErr) {
				return nil
			}
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("media path contains a symbolic link")
		}
	}
	return nil
}

// RemoveUP deletes all on-disk media for one UP. Missing directory is fine.
func RemoveUP(dataDir, uid string) error {
	uid = strings.TrimSpace(uid)
	if uid == "" || strings.Contains(uid, "..") || strings.ContainsAny(uid, `/\`) {
		return errors.New("invalid uid")
	}
	path := pathologize.Join(filepath.Join(dataDir, "media"), string(model.PlatformBilibili), model.SourceID(model.PlatformBilibili, uid))
	if err := rejectSymlinkPath(dataDir, path, true); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing media for uid %s: %w", uid, err)
	}
	return nil
}

// RemoveSource deletes one v3 source archive without accepting path traversal.
func RemoveSource(dataDir string, platform model.Platform, sourceID string) error {
	if err := platform.Validate(); err != nil {
		return err
	}
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || strings.Contains(sourceID, "..") || strings.ContainsAny(sourceID, `/\`) {
		return errors.New("invalid source id")
	}
	path := pathologize.Join(filepath.Join(dataDir, "media"), string(platform), sourceID)
	if err := rejectSymlinkPath(dataDir, path, true); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing media for source %s: %w", sourceID, err)
	}
	return nil
}

// ReadFile resolves and reads a local media file.
func ReadFile(dataDir, rel string) (data []byte, contentType string, err error) {
	abs, err := Resolve(dataDir, rel)
	if err != nil {
		return nil, "", err
	}
	if err := rejectSymlinkPath(dataDir, abs, false); err != nil {
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

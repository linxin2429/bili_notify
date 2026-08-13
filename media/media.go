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
)

const (
	// MaxFileSize is the hard download limit per media item.
	MaxFileSize int64 = 10 << 20
	// WeComMaxImageSize is the WeCom robot image payload limit (raw bytes).
	WeComMaxImageSize    int64 = 2 << 20
	FeishuMaxFileSize    int64 = 30 << 20
	WeComMaxFileSize     int64 = 20 << 20
	MicrosoftMaxFileSize int64 = 150 << 20
)

// ErrTooLarge is returned when a remote object exceeds MaxFileSize.
var ErrTooLarge = errors.New("media exceeds size limit")

// OpenFile opens an archived media file without following symbolic links out
// of data_dir/media. The caller owns the returned file.
func OpenFile(dataDir, relative string) (*os.File, int64, string, error) {
	abs, err := Resolve(dataDir, relative)
	if err != nil {
		return nil, 0, "", err
	}
	mediaRoot, err := filepath.Abs(filepath.Join(dataDir, "media"))
	if err != nil {
		return nil, 0, "", err
	}
	rel, err := filepath.Rel(mediaRoot, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, 0, "", errors.New("media path is outside media directory")
	}
	if err := rejectSymlinkPath(dataDir, abs, false); err != nil {
		return nil, 0, "", err
	}
	file, err := os.Open(abs)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, "", err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, "", errors.New("media path is not a regular file")
	}
	buffer := make([]byte, 512)
	read, readErr := file.Read(buffer)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		_ = file.Close()
		return nil, 0, "", readErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, 0, "", err
	}
	contentType := "application/octet-stream"
	if read > 0 {
		contentType = http.DetectContentType(buffer[:read])
	}
	return file, info.Size(), contentType, nil
}

// PublicTransport applies the media SSRF policy at the innermost transport.
// A platform RequestGate can then wrap this transport so API and media traffic
// share one complete-lifecycle request budget.
func PublicTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DialTLSContext = nil
	transport.DialContext = securePublicDial
	return transport
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

package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/linxin2429/bili_notify/model"
	"github.com/spf13/pathologize"
)

var ErrBudgetExhausted = errors.New("media storage budget exhausted")

type AttachmentDownloader struct {
	DataDir             string
	Client              *http.Client
	UserAgent           string
	AllowPrivateNetwork bool
	mu                  sync.Mutex
}

type AttachmentResult struct {
	Downloaded int
	Failed     int
	Skipped    int
	Bytes      int64
	BudgetFull bool
}

// EnsureAttachments localizes arbitrary attachment types with streaming size
// enforcement. Signed remote URLs remain only in the caller's in-memory model.
func (downloader *AttachmentDownloader) EnsureAttachments(ctx context.Context, platform model.Platform, sourceID, contentID string, attachments []model.Attachment, maxFileBytes, totalBudgetBytes int64, apiCookies map[string]string) AttachmentResult {
	var result AttachmentResult
	if downloader == nil || maxFileBytes <= 0 || totalBudgetBytes <= 0 {
		return result
	}
	downloader.mu.Lock()
	defer downloader.mu.Unlock()
	used, err := directorySize(filepath.Join(downloader.DataDir, "media", string(platform)))
	if err != nil {
		result.Failed = len(attachments)
		for index := range attachments {
			attachments[index].LocalizeError = publicAssetError(err)
		}
		return result
	}
	for index := range attachments {
		item := &attachments[index]
		if item.Type == model.AttachmentLink || item.LocalPath != "" || item.RemoteURL == "" {
			result.Skipped++
			continue
		}
		remaining := totalBudgetBytes - used
		if remaining <= 0 {
			item.LocalizeError = ErrBudgetExhausted.Error()
			result.BudgetFull = true
			result.Skipped++
			continue
		}
		limit := min(maxFileBytes, remaining)
		size, err := downloader.downloadAttachment(ctx, platform, sourceID, contentID, item, limit, apiCookies)
		if err != nil {
			if errors.Is(err, ErrTooLarge) && remaining < maxFileBytes {
				err = ErrBudgetExhausted
			}
			item.LocalizeError = publicAssetError(err)
			if errors.Is(err, ErrBudgetExhausted) {
				result.BudgetFull = true
				result.Skipped++
			} else {
				result.Failed++
			}
			continue
		}
		item.LocalizeError = ""
		result.Downloaded++
		result.Bytes += size
		used += size
	}
	return result
}

func (downloader *AttachmentDownloader) downloadAttachment(ctx context.Context, platform model.Platform, sourceID, contentID string, item *model.Attachment, limit int64, apiCookies map[string]string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, item.RemoteURL, nil)
	if err != nil {
		return 0, err
	}
	if err := validateRemoteURL(ctx, request.URL, downloader.AllowPrivateNetwork); err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", firstNonEmpty(downloader.UserAgent, "bili-notify"))
	request.Header.Set("Accept", "*/*")
	if platform == model.PlatformZSXQ && strings.EqualFold(request.URL.Hostname(), "api.zsxq.com") {
		for name, value := range apiCookies {
			request.AddCookie(&http.Cookie{Name: name, Value: value})
		}
	}
	client := downloader.safeClient(ctx)
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("attachment HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return 0, ErrTooLarge
	}
	fileName := pathologize.Clean(filepath.Base(strings.ReplaceAll(firstNonEmpty(item.FileName, item.ExternalID, "attachment"), `\`, "/")))
	externalID := pathologize.Clean(filepath.Base(strings.ReplaceAll(firstNonEmpty(item.ExternalID, "attachment"), `\`, "/")))
	rel := filepath.ToSlash(pathologize.Join("media", string(platform), sourceID, contentID, externalID+"-"+fileName))
	abs, err := Resolve(downloader.DataDir, rel)
	if err != nil {
		return 0, err
	}
	if err := rejectSymlinkPath(downloader.DataDir, filepath.Dir(abs), true); err != nil {
		return 0, err
	}
	temporary, err := os.CreateTemp(downloader.DataDir, ".asset-*")
	if err != nil {
		return 0, err
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return 0, err
	}
	written, err := io.Copy(temporary, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, err
	}
	if written > limit {
		return 0, ErrTooLarge
	}
	if written == 0 {
		return 0, errors.New("empty attachment")
	}
	if err := temporary.Sync(); err != nil {
		return 0, err
	}
	if err := temporary.Close(); err != nil {
		return 0, err
	}
	finalRelative, err := moveIntoMedia(downloader.DataDir, temporaryName, abs)
	if err != nil {
		return 0, err
	}
	committed = true
	item.LocalPath = finalRelative
	item.Size = written
	if item.MIME == "" {
		item.MIME = response.Header.Get("Content-Type")
	}
	return written, nil
}

func (downloader *AttachmentDownloader) safeClient(ctx context.Context) *http.Client {
	base := downloader.Client
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	// Never inherit a jar: redirects to a CDN must not receive ZSXQ cookies.
	copy.Jar = nil
	previous := base.CheckRedirect
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		// Strip all sensitive request state on every hop. Cookies may be added
		// only to an original api.zsxq.com request above.
		request.Header.Del("Cookie")
		request.Header.Del("Authorization")
		return validateRemoteURL(ctx, request.URL, downloader.AllowPrivateNetwork)
	}
	if !downloader.AllowPrivateNetwork {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if configured, ok := base.Transport.(*http.Transport); ok {
			transport = configured.Clone()
		}
		transport.Proxy = nil
		transport.DialTLSContext = nil
		transport.DialContext = securePublicDial
		copy.Transport = transport
	}
	return &copy
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("media tree contains a symbolic link")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func publicAssetError(err error) string {
	if errors.Is(err, ErrTooLarge) {
		return ErrTooLarge.Error()
	}
	if errors.Is(err, ErrBudgetExhausted) {
		return ErrBudgetExhausted.Error()
	}
	return "attachment localization failed"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func remoteURLHost(raw string) string {
	parsed, _ := url.Parse(raw)
	return parsed.Hostname()
}

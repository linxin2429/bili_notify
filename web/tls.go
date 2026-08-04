package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type certificateReloader struct {
	certFile string
	keyFile  string
	logger   *slog.Logger
	current  atomic.Pointer[tls.Certificate]
}

func newCertificateReloader(certFile, keyFile string, logger *slog.Logger) (*certificateReloader, error) {
	var err error
	certFile, err = filepath.Abs(certFile)
	if err != nil {
		return nil, fmt.Errorf("resolving certificate path: %w", err)
	}
	keyFile, err = filepath.Abs(keyFile)
	if err != nil {
		return nil, fmt.Errorf("resolving private key path: %w", err)
	}
	r := &certificateReloader{certFile: certFile, keyFile: keyFile, logger: logger}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certificateReloader) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert := r.current.Load()
			if cert == nil {
				return nil, errors.New("TLS certificate is unavailable")
			}
			return cert, nil
		},
	}
}

func (r *certificateReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("loading TLS key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("parsing TLS certificate: %w", err)
	}
	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return fmt.Errorf("TLS certificate is not currently valid: %s to %s", leaf.NotBefore, leaf.NotAfter)
	}
	cert.Leaf = leaf
	r.current.Store(&cert)
	return nil
}

func (r *certificateReloader) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating certificate watcher: %w", err)
	}
	defer watcher.Close()
	dirs := map[string]bool{filepath.Dir(r.certFile): true, filepath.Dir(r.keyFile): true}
	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("watching certificate directory %s: %w", dir, err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				r.logger.Warn("TLS certificate watcher error", "err", err)
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != r.certFile && event.Name != r.keyFile {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if err := r.reload(); err != nil {
				r.logger.Warn("rejected replacement TLS certificate", "err", err)
				continue
			}
			r.logger.Info("TLS certificate reloaded")
		}
	}
}

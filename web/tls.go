package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func loadOrCreateTLSConfig(path string) (*tls.Config, error) {
	bundle, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		bundle, err = generateSelfSignedBundle()
		if err != nil {
			return nil, err
		}
		if err := writeExclusive(path, bundle, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading TLS bundle: %w", err)
	} else {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("checking TLS bundle permissions: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("TLS bundle permissions are %o, want 600", info.Mode().Perm())
		}
	}
	certificate, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		return nil, fmt.Errorf("loading TLS bundle: %w", err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parsing TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("TLS certificate is not currently valid: %s to %s", leaf.NotBefore, leaf.NotAfter)
	}
	certificate.Leaf = leaf
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, nil
}

func generateSelfSignedBundle() ([]byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating TLS private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generating TLS certificate serial: %w", err)
	}
	hostname, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	if hostname != "" && hostname != "localhost" {
		dnsNames = append(dnsNames, hostname)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Bili Notify"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("creating TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshaling TLS private key: %w", err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	return bundle, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating TLS directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("creating TLS bundle: %w", err)
	}
	writeErr := error(nil)
	if _, err := file.Write(data); err != nil {
		writeErr = fmt.Errorf("writing TLS bundle: %w", err)
	} else if err := file.Sync(); err != nil {
		writeErr = fmt.Errorf("syncing TLS bundle: %w", err)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("closing TLS bundle: %w", closeErr)
	}
	return nil
}

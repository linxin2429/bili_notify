package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type smtpCapture struct {
	commands []string
	message  string
	err      error
}

type smtpServerOptions struct {
	implicitTLS bool
	rejectAuth  bool
	rejectRCPT  bool
	disconnect  string
}

func TestEmailSenderRealSMTPContracts(t *testing.T) {
	tests := []struct {
		name     string
		implicit bool
	}{
		{name: "implicit TLS", implicit: true},
		{name: "STARTTLS", implicit: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			serverTLS, roots := smtpTestCertificate(t)
			endpoint, captures := startSMTPServer(t, serverTLS, smtpServerOptions{implicitTLS: tt.implicit})
			host, port := splitSMTPAddress(t, endpoint)
			dataDir := t.TempDir()
			relative := filepath.Join("media", "1", "2", "image.png")
			absolute := filepath.Join(dataDir, relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(absolute), 0o700))
			require.NoError(t, os.WriteFile(absolute, []byte("\x89PNG\r\n\x1a\ninline-image"), 0o600))
			fileRelative := filepath.Join("media", "1", "2", "archived-report.txt")
			require.NoError(t, os.WriteFile(filepath.Join(dataDir, fileRelative), []byte("attachment-body"), 0o600))
			mode := "starttls"
			if tt.implicit {
				mode = "tls"
			}
			sender, err := newEmailSenderWithTLSConfig(map[string]string{
				"host": host, "port": port, "tls": mode,
				"username": "mailer", "password": "smtp-password",
				"from": "sender@example.com", "to": "one@example.com, Two <two@example.com>",
			}, dataDir, &tls.Config{RootCAs: roots, ServerName: host})
			require.NoError(t, err)
			message := Message{Subject: "协议测试", Sections: []Section{{
				Paragraphs: []string{"plain and html"},
				Images:     []Image{{Label: "inline", URL: "https://example.invalid/fallback.png", LocalPath: filepath.ToSlash(relative), ContentType: "image/png"}},
			}}, Files: []model.DeliveryFile{{ID: "file-1", Name: "original-report.txt", MIME: "text/plain", LocalPath: filepath.ToSlash(fileRelative)}}}
			sendErr := sender.Send(t.Context(), message)
			capture := receiveSMTPCapture(t, captures)
			require.NoError(t, sendErr, "SMTP capture: %+v", capture)
			require.NoError(t, capture.err)
			assert.Contains(t, capture.commands, "AUTH PLAIN")
			assert.Contains(t, capture.commands, "RCPT TO:<one@example.com>")
			assert.Contains(t, capture.commands, "RCPT TO:<two@example.com>")
			assert.Contains(t, capture.message, "multipart/alternative")
			assert.Contains(t, capture.message, "Content-Id: image-0")
			assert.Contains(t, capture.message, "cid:image-0")
			assert.Contains(t, capture.message, "original-report.txt")
			assert.Contains(t, capture.message, base64.StdEncoding.EncodeToString([]byte("attachment-body")))
			assert.NotContains(t, capture.message, "smtp-password")
		})
	}
}

func TestEmailSenderRealSMTPFailures(t *testing.T) {
	tests := []struct {
		name        string
		options     smtpServerOptions
		untrusted   bool
		want        string
		wantCapture bool
	}{
		{name: "certificate verification", untrusted: true, want: "sending email"},
		{name: "authentication rejected", options: smtpServerOptions{rejectAuth: true}, want: "sending email", wantCapture: true},
		{name: "recipient rejected", options: smtpServerOptions{rejectRCPT: true}, want: "sending email", wantCapture: true},
		{name: "disconnect during DATA", options: smtpServerOptions{disconnect: "DATA"}, want: "sending email", wantCapture: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			serverTLS, roots := smtpTestCertificate(t)
			endpoint, captures := startSMTPServer(t, serverTLS, tt.options)
			host, port := splitSMTPAddress(t, endpoint)
			clientRoots := roots
			if tt.untrusted {
				clientRoots = x509.NewCertPool()
			}
			sender, err := newEmailSenderWithTLSConfig(map[string]string{
				"host": host, "port": port, "tls": "starttls", "username": "mailer", "password": "smtp-password",
				"from": "sender@example.com", "to": "one@example.com",
			}, "", &tls.Config{RootCAs: clientRoots, ServerName: host})
			require.NoError(t, err)
			err = sender.Send(t.Context(), TextMessage("subject", "body"))
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
			assert.NotContains(t, err.Error(), "smtp-password")
			if tt.wantCapture {
				capture := receiveSMTPCapture(t, captures)
				assert.Error(t, capture.err)
			}
		})
	}
}

func startSMTPServer(t *testing.T, serverTLS *tls.Config, options smtpServerOptions) (string, <-chan smtpCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	captures := make(chan smtpCapture, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			captures <- smtpCapture{err: acceptErr}
			return
		}
		if options.implicitTLS {
			connection = tls.Server(connection, serverTLS)
		}
		capture := serveSMTPConnection(connection, serverTLS, options)
		_ = connection.Close()
		captures <- capture
	}()
	return listener.Addr().String(), captures
}

func serveSMTPConnection(connection net.Conn, serverTLS *tls.Config, options smtpServerOptions) smtpCapture {
	capture := smtpCapture{}
	reader := textproto.NewReader(bufio.NewReader(connection))
	writer := bufio.NewWriter(connection)
	write := func(line string) error {
		if _, err := fmt.Fprintf(writer, "%s\r\n", line); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("220 smtp.local ESMTP"); err != nil {
		capture.err = err
		return capture
	}
	for {
		line, err := reader.ReadLine()
		if err != nil {
			capture.err = err
			return capture
		}
		command, _, _ := strings.Cut(line, " ")
		upper := strings.ToUpper(command)
		if upper != "AUTH" {
			capture.commands = append(capture.commands, line)
		}
		switch upper {
		case "EHLO":
			if _, ok := connection.(*tls.Conn); ok {
				err = write("250-smtp.local\r\n250-AUTH PLAIN\r\n250 8BITMIME")
			} else {
				err = write("250-smtp.local\r\n250-STARTTLS\r\n250 AUTH PLAIN")
			}
		case "STARTTLS":
			err = write("220 ready for TLS")
			if err == nil {
				connection = tls.Server(connection, serverTLS)
				reader = textproto.NewReader(bufio.NewReader(connection))
				writer = bufio.NewWriter(connection)
			}
		case "AUTH":
			capture.commands = append(capture.commands, "AUTH PLAIN")
			if options.rejectAuth {
				err = write("535 authentication rejected")
				capture.err = fmt.Errorf("authentication rejected")
				return capture
			}
			encoded := strings.TrimSpace(strings.TrimPrefix(line, command+" PLAIN"))
			decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
			if decodeErr != nil || !strings.Contains(string(decoded), "mailer") {
				err = write("535 invalid credentials")
			} else {
				err = write("235 authenticated")
			}
		case "MAIL":
			err = write("250 sender accepted")
		case "NOOP":
			err = write("250 ok")
		case "RSET":
			err = write("250 reset")
		case "RCPT":
			if options.rejectRCPT {
				err = write("550 recipient rejected")
				capture.err = fmt.Errorf("recipient rejected")
				return capture
			}
			err = write("250 recipient accepted")
		case "DATA":
			if options.disconnect == "DATA" {
				capture.err = fmt.Errorf("connection reset during DATA")
				return capture
			}
			err = write("354 end with dot")
			if err == nil {
				data, readErr := io.ReadAll(reader.DotReader())
				capture.message = string(data)
				err = readErr
			}
			if err == nil {
				err = write("250 queued")
			}
		case "QUIT":
			_ = write("221 bye")
			return capture
		default:
			err = write("502 unsupported")
		}
		if err != nil {
			capture.err = err
			return capture
		}
	}
}

func smtpTestCertificate(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}, roots
}

func splitSMTPAddress(t *testing.T, endpoint string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(endpoint)
	require.NoError(t, err)
	return host, port
}

func receiveSMTPCapture(t *testing.T, captures <-chan smtpCapture) smtpCapture {
	t.Helper()
	select {
	case capture := <-captures:
		return capture
	case <-time.After(5 * time.Second):
		require.FailNow(t, "SMTP server did not finish")
		return smtpCapture{}
	}
}

func TestEmailSenderCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sender, err := newEmailSender(emailTestSettings("from@example.com", "to@example.com"), "")
	require.NoError(t, err)
	err = sender.Send(ctx, TextMessage("subject", "body"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "sending email")
}

package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linxin2429/bili_notify/state"
	"golang.org/x/crypto/argon2"
)

const sessionCookie = "bili_notify_session"

type authSession struct {
	CSRF       string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type authenticator struct {
	store     *state.Store
	setupCode string
	mu        sync.Mutex
	sessions  map[string]authSession
	failures  map[string][]time.Time
}

func newAuthenticator(store *state.Store) (*authenticator, string, error) {
	a := &authenticator{store: store, sessions: make(map[string]authSession), failures: make(map[string][]time.Time)}
	hash, err := store.AdminPasswordHash()
	if err == nil {
		if _, err := parsePasswordHash(hash); err != nil {
			return nil, "", fmt.Errorf("invalid administrator password hash: %w", err)
		}
		return a, "", nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return nil, "", err
	}
	code, err := randomSetupCode()
	if err != nil {
		return nil, "", fmt.Errorf("generating setup code: %w", err)
	}
	a.setupCode = code
	return a, code, nil
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

type parsedHash struct {
	memory uint32
	time   uint32
	lanes  uint8
	salt   []byte
	hash   []byte
}

func parsePasswordHash(encoded string) (parsedHash, error) {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return parsedHash{}, errors.New("expected Argon2id PHC string")
	}
	var p parsedHash
	var lanes uint64
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &lanes); err != nil || lanes > 255 {
		return parsedHash{}, errors.New("invalid Argon2id parameters")
	}
	p.lanes = uint8(lanes)
	var err error
	p.salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return parsedHash{}, errors.New("invalid Argon2id salt")
	}
	p.hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(p.hash) < 16 {
		return parsedHash{}, errors.New("invalid Argon2id hash")
	}
	if p.memory < 32*1024 || p.time < 2 || p.lanes == 0 {
		return parsedHash{}, errors.New("Argon2id parameters are too weak")
	}
	return p, nil
}

func verifyPassword(encoded, password string) bool {
	p, err := parsePasswordHash(encoded)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.lanes, uint32(len(p.hash)))
	return subtle.ConstantTimeCompare(actual, p.hash) == 1
}

func (a *authenticator) initialized() (bool, error) {
	_, err := a.store.AdminPasswordHash()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, state.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (a *authenticator) initialize(code, password string) error {
	a.mu.Lock()
	validCode := subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(code))), []byte(a.setupCode)) == 1 && a.setupCode != ""
	a.mu.Unlock()
	if !validCode {
		return errors.New("invalid setup code")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := a.store.InitializeAdminPasswordHash(hash); err != nil {
		return err
	}
	a.mu.Lock()
	a.setupCode = ""
	a.mu.Unlock()
	return nil
}

func (a *authenticator) authenticate(password string) bool {
	hash, err := a.store.AdminPasswordHash()
	return err == nil && verifyPassword(hash, password)
}

func (a *authenticator) changePassword(current, replacement string) error {
	if !a.authenticate(current) {
		return errors.New("current password is incorrect")
	}
	hash, err := HashPassword(replacement)
	if err != nil {
		return err
	}
	return a.store.SetAdminPasswordHash(hash)
}

func (a *authenticator) loginAllowed(remoteAddr string) bool {
	host := remoteHost(remoteAddr)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, key := range []string{host, "*"} {
		recent := a.failures[key][:0]
		for _, at := range a.failures[key] {
			if now.Sub(at) < time.Minute {
				recent = append(recent, at)
			}
		}
		a.failures[key] = recent
	}
	return len(a.failures[host]) < 5 && len(a.failures["*"]) < 20
}

func (a *authenticator) recordFailure(remoteAddr string) {
	host := remoteHost(remoteAddr)
	a.mu.Lock()
	a.failures[host] = append(a.failures[host], time.Now())
	a.failures["*"] = append(a.failures["*"], time.Now())
	a.mu.Unlock()
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func (a *authenticator) createSession() (token, csrf string, err error) {
	token, err = randomHex(32)
	if err != nil {
		return "", "", err
	}
	csrf, err = randomHex(24)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	a.mu.Lock()
	a.sessions[token] = authSession{CSRF: csrf, CreatedAt: now, LastSeenAt: now}
	a.mu.Unlock()
	return token, csrf, nil
}

func (a *authenticator) validate(r *http.Request) (string, authSession, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", authSession{}, false
	}
	session, ok := a.validateToken(cookie.Value, true)
	return cookie.Value, session, ok
}

func (a *authenticator) validateToken(token string, touch bool) (authSession, bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[token]
	if !ok || now.Sub(session.CreatedAt) > 24*time.Hour || now.Sub(session.LastSeenAt) > 8*time.Hour {
		delete(a.sessions, token)
		return authSession{}, false
	}
	if touch {
		session.LastSeenAt = now
		a.sessions[token] = session
	}
	return session, true
}

func (a *authenticator) logout(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
		return cookie.Value
	}
	return ""
}

func (a *authenticator) invalidateAll() {
	a.mu.Lock()
	clear(a.sessions)
	a.mu.Unlock()
}

func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomSetupCode() (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := make([]byte, len(raw))
	for i, value := range raw {
		code[i] = alphabet[int(value)&31]
	}
	return string(code), nil
}

func secureCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

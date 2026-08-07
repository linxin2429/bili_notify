package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/linxin2429/bili_notify/app"
	"github.com/linxin2429/bili_notify/config"
)

const (
	e2eAccountUID = "100"
	e2eUPUID      = "42"
)

type upstreamState struct {
	mu             sync.Mutex
	counts         map[string]int
	messages       []map[string]any
	unexpected     []string
	newDynamic     bool
	webhookMode    string
	application    *applicationManager
	controlToken   string
	server         *httptest.Server
	upstreamClient *http.Client
}

type applicationManager struct {
	mu          sync.Mutex
	root        context.Context
	dataDir     string
	adminAddr   string
	observeAddr string
	upstream    *upstreamState
	logger      *slog.Logger
	cancel      context.CancelFunc
	done        chan error
	generation  int
	fatal       chan error
}

type logCapture struct {
	mu        sync.RWMutex
	setupCode string
}

type readyManifest struct {
	Event        string `json:"event"`
	AdminURL     string `json:"admin_url"`
	ObserveURL   string `json:"observe_url"`
	WebhookURL   string `json:"webhook_url"`
	ControlURL   string `json:"control_url"`
	ControlToken string `json:"control_token"`
	SetupCode    string `json:"setup_code"`
}

func main() {
	dataDir := flag.String("data-dir", "", "persistent application data directory")
	flag.Parse()
	if strings.TrimSpace(*dataDir) == "" {
		fatal(errors.New("--data-dir is required"))
	}

	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	capture := new(logCapture)
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, capture), &slog.HandlerOptions{Level: slog.LevelWarn}))
	upstream, err := newUpstreamState()
	if err != nil {
		fatal(err)
	}
	manager := &applicationManager{
		root: root, dataDir: *dataDir, upstream: upstream, logger: logger,
		fatal: make(chan error, 1),
	}
	upstream.application = manager
	if err := manager.startInitial(); err != nil {
		upstream.Close()
		fatal(err)
	}
	setupCode, err := capture.waitSetupCode(root, 10*time.Second)
	if err != nil {
		_ = manager.stop()
		upstream.Close()
		fatal(err)
	}
	manifest := readyManifest{
		Event: "e2e_ready", AdminURL: "https://" + manager.adminAddr, ObserveURL: "http://" + manager.observeAddr,
		WebhookURL: upstream.server.URL + "/webhook", ControlURL: upstream.server.URL + "/__e2e",
		ControlToken: upstream.controlToken, SetupCode: setupCode,
	}
	if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
		_ = manager.stop()
		upstream.Close()
		fatal(fmt.Errorf("writing ready manifest: %w", err))
	}

	select {
	case <-root.Done():
	case err := <-manager.fatal:
		_, _ = fmt.Fprintf(os.Stderr, "application stopped unexpectedly: %v\n", err)
	}
	if err := manager.stop(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "stopping application: %v\n", err)
	}
	upstream.Close()
}

func newUpstreamState() (*upstreamState, error) {
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generating control token: %w", err)
	}
	state := &upstreamState{
		counts: make(map[string]int), webhookMode: "success",
		controlToken: hex.EncodeToString(tokenBytes),
	}
	state.server = httptest.NewTLSServer(state.handler())
	state.upstreamClient = state.server.Client()
	state.upstreamClient.Timeout = 10 * time.Second
	return state, nil
}

func (s *upstreamState) Close() {
	s.server.Close()
}

func (s *upstreamState) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x/passport-login/web/qrcode/generate", s.generateQR)
	mux.HandleFunc("GET /x/passport-login/web/qrcode/poll", s.pollQR)
	mux.HandleFunc("GET /x/web-interface/nav", s.navigation)
	mux.HandleFunc("GET /x/relation/relations", s.relations)
	mux.HandleFunc("GET /x/polymer/web-dynamic/v1/feed/space", s.spaceFeed)
	mux.HandleFunc("GET /x/polymer/web-dynamic/v1/feed/all/update", s.feedUpdate)
	mux.HandleFunc("GET /x/polymer/web-dynamic/v1/feed/all", s.aggregateFeed)
	mux.HandleFunc("GET /x/v2/reply", s.rootReplies)
	mux.HandleFunc("POST /webhook", s.webhook)
	mux.HandleFunc("GET /__e2e/state", s.authorizeControl(s.controlState))
	mux.HandleFunc("PUT /__e2e/feed", s.authorizeControl(s.setFeed))
	mux.HandleFunc("PUT /__e2e/webhook", s.authorizeControl(s.setWebhook))
	mux.HandleFunc("POST /__e2e/restart", s.authorizeControl(s.restartApplication))
	return mux
}

func (s *upstreamState) authorizeControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.controlToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *upstreamState) generateQR(w http.ResponseWriter, _ *http.Request) {
	s.increment("qr_generate")
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		"url": "https://example.invalid/e2e-login", "qrcode_key": "e2e-login",
	}})
}

func (s *upstreamState) pollQR(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("qrcode_key") != "e2e-login" {
		s.unexpectedRequest(w, r, "unexpected QR key")
		return
	}
	polls := s.increment("qr_poll")
	code := 86090
	if polls >= 2 {
		code = 0
		http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "e2e-session", Path: "/", Secure: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{"code": code}})
}

func (s *upstreamState) navigation(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	s.increment("navigation")
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		"isLogin": true, "mid": 100, "uname": "E2E Account",
	}})
}

func (s *upstreamState) relations(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	if r.URL.Query().Get("fids") != e2eUPUID {
		s.unexpectedRequest(w, r, "unexpected relation targets")
		return
	}
	s.increment("relations")
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		e2eUPUID: map[string]any{"attribute": 2},
	}})
}

func (s *upstreamState) spaceFeed(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	query := r.URL.Query()
	if query.Get("host_mid") != e2eUPUID || !strings.Contains(query.Get("features"), "onlyfansAssetsV2") {
		s.unexpectedRequest(w, r, "invalid space feed query")
		return
	}
	s.increment("space_feed")
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		"has_more": false, "offset": "", "items": []any{dynamicFixture("baseline-1", "baseline content", 1700000000)},
	}})
}

func (s *upstreamState) feedUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	baseline := r.URL.Query().Get("update_baseline")
	s.mu.Lock()
	s.counts["feed_update"]++
	available := s.newDynamic && baseline == "cursor-0"
	s.mu.Unlock()
	if baseline != "cursor-0" && baseline != "cursor-1" {
		s.unexpectedRequest(w, r, "unexpected aggregate update baseline")
		return
	}
	updateNum := 0
	if available {
		updateNum = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{"update_num": updateNum}})
}

func (s *upstreamState) aggregateFeed(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	query := r.URL.Query()
	if !strings.Contains(query.Get("features"), "onlyfansAssetsV2") || query.Get("type") != "all" {
		s.unexpectedRequest(w, r, "invalid aggregate feed query")
		return
	}
	baseline := query.Get("update_baseline")
	if baseline == "" {
		s.increment("feed_initialize")
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
			"has_more": false, "offset": "", "update_baseline": "cursor-0", "update_num": 0, "items": []any{},
		}})
		return
	}
	s.mu.Lock()
	s.counts["feed_fetch"]++
	available := s.newDynamic && baseline == "cursor-0"
	s.mu.Unlock()
	if !available {
		s.unexpectedRequest(w, r, "aggregate feed fetched without an available update")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		"has_more": false, "offset": "", "update_baseline": "cursor-1", "update_num": 1,
		"items": []any{dynamicFixture("dynamic-2", "new dynamic content", 1700000001)},
	}})
}

func (s *upstreamState) webhook(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		s.unexpectedRequest(w, r, "invalid webhook JSON")
		return
	}
	s.mu.Lock()
	s.counts["webhook"]++
	s.messages = append(s.messages, payload)
	mode := s.webhookMode
	s.mu.Unlock()
	if mode == "permanent_failure" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errcode": 40001, "errmsg": "e2e permanent failure"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"errcode": 0, "errmsg": "ok"})
}

func (s *upstreamState) rootReplies(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(w, r) {
		return
	}
	s.increment("comment_root")
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "0", "data": map[string]any{
		"page": map[string]any{"num": 1, "size": 20, "count": 0}, "replies": []any{},
	}})
}

func (s *upstreamState) authenticated(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("SESSDATA")
	if err != nil || cookie.Value != "e2e-session" {
		s.unexpectedRequest(w, r, "missing E2E session cookie")
		return false
	}
	return true
}

func (s *upstreamState) controlState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	counts := make(map[string]int, len(s.counts))
	for key, value := range s.counts {
		counts[key] = value
	}
	messages := append([]map[string]any(nil), s.messages...)
	unexpected := append([]string(nil), s.unexpected...)
	newDynamic := s.newDynamic
	webhookMode := s.webhookMode
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"counts": counts, "messages": messages, "unexpected": unexpected,
		"new_dynamic": newDynamic, "webhook_mode": webhookMode, "generation": s.application.Generation(),
	})
}

func (s *upstreamState) setFeed(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NewDynamic bool `json:"new_dynamic"`
	}
	if err := decodeJSON(r, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.newDynamic = input.NewDynamic
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *upstreamState) setWebhook(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if input.Mode != "success" && input.Mode != "permanent_failure" {
		http.Error(w, "mode must be success or permanent_failure", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.webhookMode = input.Mode
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *upstreamState) restartApplication(w http.ResponseWriter, _ *http.Request) {
	if err := s.application.Restart(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"generation": s.application.Generation()})
}

func (s *upstreamState) increment(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[name]++
	return s.counts[name]
}

func (s *upstreamState) unexpectedRequest(w http.ResponseWriter, r *http.Request, reason string) {
	message := fmt.Sprintf("%s %s: %s", r.Method, r.URL.RequestURI(), reason)
	s.mu.Lock()
	s.unexpected = append(s.unexpected, message)
	s.mu.Unlock()
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": message})
}

func dynamicFixture(id, summary string, timestamp int64) map[string]any {
	return map[string]any{
		"id_str": id, "type": "DYNAMIC_TYPE_WORD",
		"modules": map[string]any{
			"module_author":  map[string]any{"mid": 42, "name": "E2E UP", "pub_ts": timestamp},
			"module_dynamic": map[string]any{"desc": map[string]any{"text": summary}, "major": nil},
		},
	}
}

func (m *applicationManager) startInitial() error {
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("binding initial admin listener: %w", err)
	}
	observe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.Join(fmt.Errorf("binding initial observability listener: %w", err), admin.Close())
	}
	m.adminAddr = admin.Addr().String()
	m.observeAddr = observe.Addr().String()
	return m.start(admin, observe)
}

func (m *applicationManager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.stopLocked(); err != nil {
		return err
	}
	admin, observe, err := m.rebind()
	if err != nil {
		return err
	}
	return m.startLocked(admin, observe)
}

func (m *applicationManager) Generation() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

func (m *applicationManager) start(admin, observe net.Listener) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked(admin, observe)
}

func (m *applicationManager) startLocked(admin, observe net.Listener) error {
	ctx, cancel := context.WithCancel(m.root)
	done := make(chan error, 1)
	m.cancel = cancel
	m.done = done
	m.generation++
	dependencies := app.Dependencies{
		BilibiliHTTPClient: m.upstream.upstreamClient, NotificationHTTPClient: m.upstream.upstreamClient,
		BilibiliAPIURL: m.upstream.server.URL, BilibiliPassportURL: m.upstream.server.URL,
		Logger: m.logger, AdminListener: admin, ObserveListener: observe,
	}
	cfg := config.Config{
		DataDir: m.dataDir, AdminAddr: m.adminAddr, ObserveAddr: m.observeAddr,
		PollInterval: 10 * time.Second, RequestRate: 10, RequestConcurrency: 2, LogLevel: "warn",
		AuditLogRetention: 180 * 24 * time.Hour,
	}
	go func() {
		err := app.RunWithDependencies(ctx, cfg, "e2e", dependencies)
		done <- err
		if ctx.Err() == nil {
			select {
			case m.fatal <- err:
			default:
			}
		}
	}()
	if err := waitForHealth(ctx, "http://"+m.observeAddr+"/healthz", 10*time.Second); err != nil {
		cancel()
		<-done
		return err
	}
	return nil
}

func (m *applicationManager) stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *applicationManager) stopLocked() error {
	if m.cancel == nil {
		return nil
	}
	m.cancel()
	done := m.done
	m.cancel = nil
	m.done = nil
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	case <-time.After(20 * time.Second):
		return errors.New("application shutdown timed out")
	}
}

func (m *applicationManager) rebind() (net.Listener, net.Listener, error) {
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		admin, err := net.Listen("tcp", m.adminAddr)
		if err == nil {
			observe, observeErr := net.Listen("tcp", m.observeAddr)
			if observeErr == nil {
				return admin, observe, nil
			}
			_ = admin.Close()
			err = observeErr
		}
		select {
		case <-m.root.Done():
			return nil, nil, m.root.Err()
		case <-deadline.C:
			return nil, nil, fmt.Errorf("rebinding application listeners: %w", err)
		case <-ticker.C:
		}
	}
}

func waitForHealth(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("waiting for application health at %s", endpoint)
		case <-ticker.C:
		}
	}
}

func (w *logCapture) Write(p []byte) (int, error) {
	var record map[string]any
	if json.Unmarshal(p, &record) == nil {
		if code, ok := record["setup_code"].(string); ok && code != "" {
			w.mu.Lock()
			w.setupCode = code
			w.mu.Unlock()
		}
	}
	return len(p), nil
}

func (w *logCapture) waitSetupCode(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		w.mu.RLock()
		code := w.setupCode
		w.mu.RUnlock()
		if code != "" {
			return code, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("waiting for administrator setup code")
		case <-ticker.C:
		}
	}
}

func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

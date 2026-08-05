package web

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
)

//go:embed all:dist
var assets embed.FS

type Server struct {
	adminAddr     string
	observeAddr   string
	tlsConfig     *tls.Config
	auth          *authenticator
	engine        *service.Engine
	store         *state.Store
	events        *service.EventBus
	logger        *slog.Logger
	registry      *prometheus.Registry
	dataDir       string
	static        fs.FS
	connectionsMu sync.Mutex
	connections   map[string]map[*websocket.Conn]struct{}
}

func NewServer(adminAddr, observeAddr, tlsPath string, engine *service.Engine, store *state.Store, events *service.EventBus, logger *slog.Logger, registry *prometheus.Registry, dataDir string) (*Server, error) {
	tlsConfig, err := loadOrCreateTLSConfig(tlsPath)
	if err != nil {
		return nil, err
	}
	auth, setupCode, err := newAuthenticator(store)
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, err
	}
	if setupCode != "" {
		logger.Error("administrator setup required", "setup_code", setupCode)
	}
	return &Server{
		adminAddr: adminAddr, observeAddr: observeAddr, tlsConfig: tlsConfig, auth: auth,
		engine: engine, store: store, events: events, logger: logger, registry: registry, dataDir: dataDir, static: static,
		connections: make(map[string]map[*websocket.Conn]struct{}),
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	admin := &http.Server{
		Addr: s.adminAddr, Handler: s.adminHandler(), TLSConfig: s.tlsConfig,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	observe := &http.Server{
		Addr: s.observeAddr, Handler: s.observeHandler(),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		s.logger.Info("admin server started", "addr", s.adminAddr)
		if err := admin.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		s.logger.Info("observability server started", "addr", s.observeAddr)
		if err := observe.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		s.closeAllConnections(websocket.StatusGoingAway, "server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return errors.Join(admin.Shutdown(shutdownCtx), observe.Shutdown(shutdownCtx))
	})
	return g.Wait()
}

func (s *Server) observeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		status, err := s.engine.Status()
		if err != nil || !status.Ready {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	return mux
}

func (s *Server) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/session", s.sessionState)
	mux.HandleFunc("POST /api/v1/setup", s.setup)
	mux.HandleFunc("POST /api/v1/session", s.login)
	mux.HandleFunc("DELETE /api/v1/session", s.requireSession(true, s.logout))
	mux.HandleFunc("PUT /api/v1/session/password", s.requireSession(true, s.changePassword))
	s.registerAdminAPI(mux)
	mux.HandleFunc("GET /api/v1/ws", s.webSocket)
	mux.Handle("GET /assets/", http.FileServer(http.FS(s.static)))
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /{path...}", s.index)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSession(csrf bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, session, ok := s.auth.validate(r)
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "authentication required")
			return
		}
		if csrf && !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRF) {
			writeAPIError(w, http.StatusForbidden, "invalid_csrf", "invalid CSRF token")
			return
		}
		next(w, r)
	}
}

func constantTimeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, "UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.auth.initialized()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "reading authentication state")
		return
	}
	response := map[string]any{"setup_required": !initialized, "authenticated": false}
	if _, session, ok := s.auth.validate(r); ok {
		response["authenticated"] = true
		response["csrf_token"] = session.CSRF
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.loginAllowed(r.RemoteAddr) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts")
		return
	}
	var request struct {
		SetupCode string `json:"setup_code"`
		Password  string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.initialize(request.SetupCode, request.Password); err != nil {
		s.auth.recordFailure(r.RemoteAddr)
		status := http.StatusBadRequest
		code := "invalid_setup"
		if errors.Is(err, state.ErrInitialized) {
			status, code = http.StatusConflict, "already_initialized"
		}
		writeAPIError(w, status, code, err.Error())
		return
	}
	s.createHTTPSession(w)
	s.logger.Info("administrator initialized", "remote_addr", r.RemoteAddr)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.loginAllowed(r.RemoteAddr) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.auth.authenticate(request.Password) {
		s.auth.recordFailure(r.RemoteAddr)
		s.logger.Warn("administrator login failed", "remote_addr", r.RemoteAddr)
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
		return
	}
	s.createHTTPSession(w)
	s.logger.Info("administrator login succeeded", "remote_addr", r.RemoteAddr)
}

func (s *Server) createHTTPSession(w http.ResponseWriter) {
	token, csrf, err := s.auth.createSession()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "creating session")
		return
	}
	http.SetCookie(w, secureCookie(sessionCookie, token, 24*60*60))
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := s.auth.logout(r)
	s.closeTokenConnections(token, websocket.StatusPolicyViolation, "session ended")
	http.SetCookie(w, secureCookie(sessionCookie, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.changePassword(request.CurrentPassword, request.NewPassword); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	s.auth.invalidateAll()
	s.closeAllConnections(websocket.StatusPolicyViolation, "password changed")
	s.createHTTPSession(w)
	s.logger.Info("administrator password changed", "remote_addr", r.RemoteAddr)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) registerConnection(token string, connection *websocket.Conn) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	if s.connections[token] == nil {
		s.connections[token] = make(map[*websocket.Conn]struct{})
	}
	s.connections[token][connection] = struct{}{}
}

func (s *Server) unregisterConnection(token string, connection *websocket.Conn) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	delete(s.connections[token], connection)
	if len(s.connections[token]) == 0 {
		delete(s.connections, token)
	}
}

func (s *Server) closeTokenConnections(token string, status websocket.StatusCode, reason string) {
	if token == "" {
		return
	}
	s.connectionsMu.Lock()
	connections := make([]*websocket.Conn, 0, len(s.connections[token]))
	for connection := range s.connections[token] {
		connections = append(connections, connection)
	}
	delete(s.connections, token)
	s.connectionsMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close(status, reason)
	}
}

func (s *Server) closeAllConnections(status websocket.StatusCode, reason string) {
	s.connectionsMu.Lock()
	var connections []*websocket.Conn
	for _, group := range s.connections {
		for connection := range group {
			connections = append(connections, connection)
		}
	}
	clear(s.connections)
	s.connectionsMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close(status, reason)
	}
}

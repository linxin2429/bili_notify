package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	qrcode "github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"
	"golang.org/x/sync/errgroup"
)

//go:embed index.html
var assets embed.FS

type Server struct {
	adminAddr   string
	observeAddr string
	certs       *certificateReloader
	auth        *authenticator
	engine      *service.Engine
	store       *state.Store
	logger      *slog.Logger
	registry    *prometheus.Registry
}

func NewServer(adminAddr, observeAddr, certFile, keyFile, passwordHash string, engine *service.Engine, store *state.Store, logger *slog.Logger, registry *prometheus.Registry) (*Server, error) {
	certs, err := newCertificateReloader(certFile, keyFile, logger)
	if err != nil {
		return nil, err
	}
	auth, err := newAuthenticator(passwordHash)
	if err != nil {
		return nil, err
	}
	return &Server{adminAddr: adminAddr, observeAddr: observeAddr, certs: certs, auth: auth, engine: engine, store: store, logger: logger, registry: registry}, nil
}

func (s *Server) Run(ctx context.Context) error {
	admin := &http.Server{
		Addr: s.adminAddr, Handler: s.adminHandler(), TLSConfig: s.certs.TLSConfig(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	observe := &http.Server{
		Addr: s.observeAddr, Handler: s.observeHandler(),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return s.certs.Run(gctx) })
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
	public := http.NewServeMux()
	public.HandleFunc("GET /{$}", s.index)
	public.HandleFunc("POST /api/v1/session", s.login)

	protected := http.NewServeMux()
	protected.HandleFunc("DELETE /api/v1/session", s.logout)
	protected.HandleFunc("GET /api/v1/status", s.status)
	protected.HandleFunc("GET /api/v1/ups", s.ups)
	protected.HandleFunc("POST /api/v1/ups", s.ups)
	protected.HandleFunc("PUT /api/v1/ups/{uid}", s.up)
	protected.HandleFunc("DELETE /api/v1/ups/{uid}", s.up)
	protected.HandleFunc("GET /api/v1/channels", s.channels)
	protected.HandleFunc("POST /api/v1/channels", s.channels)
	protected.HandleFunc("PUT /api/v1/channels/{id}", s.channel)
	protected.HandleFunc("DELETE /api/v1/channels/{id}", s.channel)
	protected.HandleFunc("POST /api/v1/channels/{id}/test", s.testChannel)
	protected.HandleFunc("POST /api/v1/channels/{id}/microsoft/login", s.microsoftLogin)
	protected.HandleFunc("GET /api/v1/channels/{id}/microsoft/login", s.microsoftLogin)
	protected.HandleFunc("DELETE /api/v1/channels/{id}/microsoft/login", s.microsoftLogin)
	protected.HandleFunc("GET /api/v1/deliveries", s.deliveries)
	protected.HandleFunc("POST /api/v1/bilibili/login", s.startBiliLogin)
	protected.HandleFunc("GET /api/v1/bilibili/login/{id}", s.pollBiliLogin)
	protected.HandleFunc("GET /api/v1/bilibili/login/{id}/qr.png", s.biliQR)
	protected.HandleFunc("DELETE /api/v1/bilibili/login/{id}", s.cancelBiliLogin)

	public.Handle("/api/v1/", s.requireAuth(protected))
	return securityHeaders(public)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.auth.validate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if r.Method != http.MethodGet && subtleStringCompare(r.Header.Get("X-CSRF-Token"), session.CSRF) == false {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func subtleStringCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range len(a) {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	b, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.auth.loginAllowed(r.RemoteAddr) {
		s.logger.Warn("admin login rate limited", "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !verifyPassword(s.auth.passwordHash, request.Password) {
		s.auth.recordFailure(r.RemoteAddr)
		s.logger.Warn("admin login failed", "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, csrf, err := s.auth.createSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "creating session")
		return
	}
	http.SetCookie(w, secureCookie(sessionCookie, token, 24*60*60))
	s.logger.Info("admin login succeeded", "remote_addr", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.logout(r)
	http.SetCookie(w, secureCookie(sessionCookie, "", -1))
	s.logger.Info("admin logout", "remote_addr", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	status, err := s.engine.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) ups(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		ups, err := s.store.ListUPs()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ups)
		return
	}
	var up model.UP
	if err := decodeJSON(r, &up); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	up.BaselineReady = false
	if err := s.store.PutUP(up); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("UP created", "uid", up.UID, "enabled", up.Enabled)
	writeJSON(w, http.StatusCreated, up)
}

func (s *Server) up(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteUP(uid); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.logger.Info("UP deleted", "uid", uid)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var up model.UP
	if err := decodeJSON(r, &up); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if up.UID != uid {
		writeError(w, http.StatusBadRequest, "uid cannot be changed")
		return
	}
	current, err := s.store.UP(uid)
	if err != nil {
		writeError(w, http.StatusNotFound, "UP not found")
		return
	}
	up.BaselineReady = current.BaselineReady
	up.LastPollAt, up.LastSuccessAt, up.LastError, up.ConsecutiveFail = current.LastPollAt, current.LastSuccessAt, current.LastError, current.ConsecutiveFail
	if err := s.store.PutUP(up); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("UP updated", "uid", up.UID, "enabled", up.Enabled)
	writeJSON(w, http.StatusOK, up)
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		channels, err := s.store.ListChannels(true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, channels)
		return
	}
	var channel model.Channel
	if err := decodeJSON(r, &channel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.PutChannel(channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("notification channel created", "channel_id", created.ID, "channel_type", created.Type, "enabled", created.Enabled)
	writeJSON(w, http.StatusCreated, created.Masked())
}

func (s *Server) channel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteChannel(id); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.engine.CancelMicrosoftLogin(id)
		s.logger.Info("notification channel deleted", "channel_id", id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var update model.Channel
	if err := decodeJSON(r, &update); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	current, err := s.store.Channel(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if update.ID != "" && update.ID != id {
		writeError(w, http.StatusBadRequest, "channel id cannot be changed")
		return
	}
	update.ID, update.CreatedAt = id, current.CreatedAt
	for key, value := range update.Settings {
		if value == "********" {
			update.Settings[key] = current.Settings[key]
		}
	}
	if current.Type == model.ChannelMicrosoft && update.Type == model.ChannelMicrosoft && microsoftIdentityChanged(current.Settings, update.Settings) {
		for _, key := range []string{"access_token", "refresh_token", "token_type", "token_expiry", "authorized"} {
			delete(update.Settings, key)
		}
	}
	updated, err := s.store.PutChannel(update)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.UnblockChannel(id)
	s.logger.Info("notification channel updated", "channel_id", updated.ID, "channel_type", updated.Type, "enabled", updated.Enabled)
	writeJSON(w, http.StatusOK, updated.Masked())
}

func microsoftIdentityChanged(current, update map[string]string) bool {
	currentTenant := strings.TrimSpace(current["tenant"])
	if currentTenant == "" {
		currentTenant = "common"
	}
	updateTenant := strings.TrimSpace(update["tenant"])
	if updateTenant == "" {
		updateTenant = "common"
	}
	return strings.TrimSpace(current["client_id"]) != strings.TrimSpace(update["client_id"]) || currentTenant != updateTenant
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.engine.TestChannel(ctx, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (s *Server) microsoftLogin(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("id")
	switch r.Method {
	case http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		session, err := s.engine.StartMicrosoftLogin(ctx, channelID)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, state.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, session)
	case http.MethodGet:
		session, err := s.engine.MicrosoftLogin(channelID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, session)
	case http.MethodDelete:
		s.engine.CancelMicrosoftLogin(channelID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) deliveries(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 1 && parsed <= 500 {
			limit = parsed
		}
	}
	deliveries, err := s.store.ListDeliveries(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, deliveries)
}

func (s *Server) startBiliLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	login, err := s.engine.StartLogin(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, login)
}

func (s *Server) pollBiliLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	login, err := s.engine.PollLogin(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, login)
}

func (s *Server) cancelBiliLogin(w http.ResponseWriter, r *http.Request) {
	s.engine.CancelLogin(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

type bufferCloser struct{ bytes.Buffer }

func (b *bufferCloser) Close() error { return nil }

func (s *Server) biliQR(w http.ResponseWriter, r *http.Request) {
	loginURL, err := s.engine.LoginURL(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	code, err := qrcode.New(loginURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generating QR code")
		return
	}
	buf := new(bufferCloser)
	writer := standard.NewWithWriter(buf, standard.WithQRWidth(8))
	if err := code.Save(writer); err != nil {
		writeError(w, http.StatusInternalServerError, "rendering QR code")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, bytes.NewReader(buf.Bytes()))
}

func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON request: multiple values")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(message)})
}

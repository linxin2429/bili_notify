package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

type requestIDKey struct{}
type auditContextKey struct{}

type auditContext struct {
	resourceID             string
	authenticatedSessionID string
	details                map[string]any
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header)}
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponse) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func (w *bufferedResponse) flush(dst http.ResponseWriter) {
	for key, values := range w.header {
		dst.Header()[key] = append([]string(nil), values...)
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(w.body.Bytes())
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(value)
	w.bytes += n
	return n, err
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := randomHex(16)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal", "generating request id")
			return
		}
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/v2/ws" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		capture := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(capture, r)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started)
		if s.logger == nil {
			return
		}
		attrs := []any{
			"event", "http.request.completed", "request_id", requestID,
			"method", r.Method, "route", routePattern(r), "status_code", status,
			"response_bytes", capture.bytes, "duration_ms", duration.Milliseconds(), "remote_ip", remoteHost(r.RemoteAddr),
		}
		switch {
		case status >= 500:
			s.logger.ErrorContext(r.Context(), "admin API request failed", append(attrs, "result", "failure")...)
		case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
			s.logger.WarnContext(r.Context(), "admin API request denied", append(attrs, "result", "denied")...)
		case status >= 400:
			s.logger.InfoContext(r.Context(), "admin API request rejected", append(attrs, "result", "failure")...)
		case duration >= time.Second:
			s.logger.WarnContext(r.Context(), "slow admin API request", append(attrs, "result", "success")...)
		default:
			s.logger.DebugContext(r.Context(), "admin API request completed", append(attrs, "result", statusResult(status))...)
		}
	})
}

func (s *Server) audit(action, resourceType, resourceParam string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := requestIDFrom(r.Context())
		stateContext := &auditContext{details: make(map[string]any)}
		if resourceParam != "" {
			stateContext.resourceID = r.PathValue(resourceParam)
		}
		r = r.WithContext(context.WithValue(r.Context(), auditContextKey{}, stateContext))

		actor := "anonymous"
		sessionID := ""
		if _, session, ok := s.auth.validate(r); ok {
			actor = "administrator"
			sessionID = session.ID
		}
		buffer := newBufferedResponse()
		next(buffer, r)
		if stateContext.authenticatedSessionID != "" {
			actor = "administrator"
			sessionID = stateContext.authenticatedSessionID
		}
		status := buffer.status
		if status == 0 {
			status = http.StatusOK
		}
		entry := state.AuditLog{
			OccurredAt: time.Now(), RequestID: requestID, Actor: actor, SessionID: sessionID,
			RemoteIP: remoteHost(r.RemoteAddr), UserAgent: truncateUTF8(r.UserAgent(), 256),
			Action: action, ResourceType: resourceType, ResourceID: stateContext.resourceID,
			Outcome: statusResult(status), HTTPMethod: r.Method, Route: routePattern(r), StatusCode: status,
			ErrorCode: responseErrorCode(buffer.body.Bytes()), DurationMS: time.Since(started).Milliseconds(), Details: stateContext.details,
		}
		stored, err := s.store.WithContext(r.Context()).AppendAudit(entry)
		if err != nil {
			s.metrics.RecordAuditWriteFailure(r.Context())
			if s.logger != nil {
				s.logger.ErrorContext(r.Context(), "administrator operation log could not be persisted",
					"event", "audit.write_failed", "request_id", requestID, "action", action, "error", err)
			}
		} else if s.auditLogger != nil {
			s.auditLogger.LogAttrs(r.Context(), slog.LevelInfo, "administrator operation completed",
				slog.String("event", "administrator.operation"), slog.Int64("audit_id", stored.ID),
				slog.String("request_id", stored.RequestID), slog.String("actor", stored.Actor),
				slog.String("session_id", stored.SessionID), slog.String("remote_ip", stored.RemoteIP),
				slog.String("action", stored.Action), slog.String("resource_type", stored.ResourceType),
				slog.String("resource_id", stored.ResourceID), slog.String("result", stored.Outcome),
				slog.Int("status_code", stored.StatusCode), slog.Int64("duration_ms", stored.DurationMS),
				slog.Any("details", stored.Details),
			)
		}
		if err == nil {
			s.events.Publish(service.TopicAuditLogs)
		}
		buffer.flush(w)
	}
}

func setAuditResourceID(r *http.Request, value string) {
	if context, ok := r.Context().Value(auditContextKey{}).(*auditContext); ok {
		context.resourceID = value
	}
}

func setAuditDetails(r *http.Request, details map[string]any) {
	if context, ok := r.Context().Value(auditContextKey{}).(*auditContext); ok {
		for key, value := range details {
			context.details[key] = value
		}
	}
}

func setAuditAuthenticatedSession(r *http.Request, sessionID string) {
	if context, ok := r.Context().Value(auditContextKey{}).(*auditContext); ok {
		context.authenticatedSessionID = sessionID
	}
}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func routePattern(r *http.Request) string {
	pattern := strings.TrimSpace(r.Pattern)
	if _, route, found := strings.Cut(pattern, " "); found {
		return route
	}
	return pattern
}

func statusResult(status int) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
		return state.AuditDenied
	}
	if status >= 200 && status < 400 {
		return state.AuditSuccess
	}
	return state.AuditFailure
}

func responseErrorCode(body []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.Error.Code
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

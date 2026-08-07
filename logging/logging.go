// Package logging owns Bili Notify's structured application log contract.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	otellog "go.opentelemetry.io/otel/log"
)

const schemaVersion = 1

var (
	secretValuePattern = regexp.MustCompile(`(?i)(access_token|refresh_token|token|secret|password|webhook|key|setup_code)=([^&\s]+)`)
	urlPattern         = regexp.MustCompile(`https?://[^\s"']+`)
)

// Set contains category-specific loggers backed by the same JSON sinks.
type Set struct {
	System *slog.Logger
	Audit  *slog.Logger
	level  *slog.LevelVar
}

// Config configures the process-wide structured log sinks.
type Config struct {
	Level    string
	Version  string
	Stdout   io.Writer
	RunID    string
	Provider otellog.LoggerProvider
}

// Open creates JSON loggers that fan out to stdout and OpenTelemetry.
func Open(cfg Config) (*Set, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	if cfg.Stdout == nil {
		return nil, errors.New("stdout writer is required")
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)
	stdoutHandler := &redactingHandler{allowSetupCode: true, next: slog.NewJSONHandler(cfg.Stdout, &slog.HandlerOptions{
		Level: levelVar,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Value.Kind() == slog.KindTime {
				return slog.String(attr.Key, attr.Value.Time().In(time.Local).Format(time.RFC3339Nano))
			}
			return attr
		},
	})}
	var handler slog.Handler = stdoutHandler
	if cfg.Provider != nil {
		otelHandler := &levelHandler{
			level: levelVar,
			next: &redactingHandler{next: otelslog.NewHandler("github.com/linxin2429/bili_notify/logging",
				otelslog.WithLoggerProvider(cfg.Provider), otelslog.WithVersion(cfg.Version))},
		}
		handler = fanoutHandler{stdoutHandler, otelHandler}
	}
	base := slog.New(handler).With(
		"log_schema", schemaVersion,
		"service", "bili-notify",
		"version", cfg.Version,
		"run_id", cfg.RunID,
	)
	return &Set{
		System: base.With("category", "system"),
		Audit:  base.With("category", "audit"),
		level:  levelVar,
	}, nil
}

// Apply changes the process log threshold immediately.
func (s *Set) Apply(level string) error {
	parsed, err := parseLevel(level)
	if err != nil {
		return err
	}
	s.level.Set(parsed)
	return nil
}

// Close is retained as a lifecycle hook; OTel flushing is owned by telemetry.Runtime.
func (s *Set) Close() error {
	return nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", value)
	}
}

type levelHandler struct {
	level slog.Leveler
	next  slog.Handler
}

type fanoutHandler []slog.Handler

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var errs []error
	for _, handler := range h {
		if handler.Enabled(ctx, record.Level) {
			errs = append(errs, handler.Handle(ctx, record.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := make(fanoutHandler, len(h))
	for i, handler := range h {
		result[i] = handler.WithAttrs(attrs)
	}
	return result
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	result := make(fanoutHandler, len(h))
	for i, handler := range h {
		result[i] = handler.WithGroup(name)
	}
	return result
}

func (h *levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level.Level() && h.next.Enabled(ctx, level)
}

func (h *levelHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, record)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{level: h.level, next: h.next.WithAttrs(attrs)}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{level: h.level, next: h.next.WithGroup(name)}
}

type redactingHandler struct {
	next           slog.Handler
	allowSetupCode bool
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, redactText(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		clean[i] = h.redactAttr(attr)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean), allowSetupCode: h.allowSetupCode}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), allowSetupCode: h.allowSetupCode}
}

func (h *redactingHandler) redactAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if sensitiveKey(attr.Key) && !(h.allowSetupCode && strings.EqualFold(attr.Key, "setup_code")) {
		return slog.String(attr.Key, "[REDACTED]")
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		for i := range group {
			group[i] = h.redactAttr(group[i])
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(group...)}
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(redactText(attr.Value.String()))
	case slog.KindAny:
		if err, ok := attr.Value.Any().(error); ok {
			attr.Value = slog.StringValue(redactText(err.Error()))
		}
	}
	return attr
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, value := range []string{"password", "secret", "token", "cookie", "authorization", "webhook", "setup_code", "csrf"} {
		if normalized == value || strings.HasSuffix(normalized, "_"+value) || strings.HasPrefix(normalized, value+"_") {
			return true
		}
	}
	return false
}

func redactText(value string) string {
	value = urlPattern.ReplaceAllString(value, "[REDACTED_URL]")
	return secretValuePattern.ReplaceAllString(value, "$1=[REDACTED]")
}

// Package logging owns Bili Notify's structured application log contract.
package logging

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const schemaVersion = 1

// Set contains category-specific loggers backed by the same JSON sinks.
type Set struct {
	System *slog.Logger
	Audit  *slog.Logger
	level  *slog.LevelVar
	sink   *runtimeSink
}

type runtimeSink struct {
	mu        sync.Mutex
	stdout    io.Writer
	filePath  string
	retention time.Duration
	file      *lumberjack.Logger
}

// Config configures the process-wide structured log sinks.
type Config struct {
	Level     string
	Version   string
	FilePath  string
	Retention time.Duration
	Stdout    io.Writer
}

// Open creates JSON loggers that write to stdout and a bounded rotating file.
func Open(cfg Config) (*Set, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	if cfg.Retention < 24*time.Hour || cfg.Retention%(24*time.Hour) != 0 {
		return nil, errors.New("log retention must be a positive whole number of days")
	}
	if strings.TrimSpace(cfg.FilePath) == "" {
		return nil, errors.New("log file path is required")
	}
	if cfg.Stdout == nil {
		return nil, errors.New("stdout writer is required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	probe, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening structured log file: %w", err)
	}
	if err := probe.Close(); err != nil {
		return nil, fmt.Errorf("closing structured log file: %w", err)
	}
	if err := os.Chmod(cfg.FilePath, 0o600); err != nil {
		return nil, fmt.Errorf("securing structured log file: %w", err)
	}

	sink := newRuntimeSink(cfg.Stdout, cfg.FilePath, cfg.Retention)
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)
	handler := slog.NewJSONHandler(sink, &slog.HandlerOptions{
		Level: levelVar,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Value.Kind() == slog.KindTime {
				return slog.String(attr.Key, attr.Value.Time().In(time.Local).Format(time.RFC3339Nano))
			}
			return attr
		},
	})
	runID, err := randomID()
	if err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("generating log run id: %w", err)
	}
	base := slog.New(handler).With(
		"log_schema", schemaVersion,
		"service", "bili-notify",
		"version", cfg.Version,
		"run_id", runID,
	)
	return &Set{
		System: base.With("category", "system"),
		Audit:  base.With("category", "audit"),
		level:  levelVar,
		sink:   sink,
	}, nil
}

func newRuntimeSink(stdout io.Writer, filePath string, retention time.Duration) *runtimeSink {
	return &runtimeSink{
		stdout: stdout, filePath: filePath, retention: retention,
		file: &lumberjack.Logger{
			Filename: filePath, MaxSize: 20, MaxBackups: 32,
			MaxAge: int(retention / (24 * time.Hour)), Compress: false,
		},
	}
}

func (s *runtimeSink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return io.MultiWriter(s.stdout, s.file).Write(data)
}

func (s *runtimeSink) SetRetention(retention time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if retention == s.retention {
		return
	}
	old := s.file
	s.file = &lumberjack.Logger{
		Filename: s.filePath, MaxSize: 20, MaxBackups: 32,
		MaxAge: int(retention / (24 * time.Hour)), Compress: false,
	}
	s.retention = retention
	_ = old.Close()
}

func (s *runtimeSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}

// Apply changes the process log threshold immediately and the file retention
// policy used by subsequent rotation maintenance.
func (s *Set) Apply(level string, retention time.Duration) error {
	parsed, err := parseLevel(level)
	if err != nil {
		return err
	}
	if retention < 24*time.Hour || retention%(24*time.Hour) != 0 {
		return errors.New("log retention must be a positive whole number of days")
	}
	s.sink.SetRetention(retention)
	s.level.Set(parsed)
	return nil
}

// Close flushes and closes the rotating file sink.
func (s *Set) Close() error {
	if s == nil || s.sink == nil {
		return nil
	}
	return s.sink.Close()
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

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

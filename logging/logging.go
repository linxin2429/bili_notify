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
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const schemaVersion = 1

// Set contains category-specific loggers backed by the same JSON sinks.
type Set struct {
	System *slog.Logger
	Audit  *slog.Logger
	file   *lumberjack.Logger
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

	rotating := &lumberjack.Logger{
		Filename: cfg.FilePath, MaxSize: 20, MaxBackups: 32,
		MaxAge: int(cfg.Retention / (24 * time.Hour)), Compress: false,
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)
	handler := slog.NewJSONHandler(io.MultiWriter(cfg.Stdout, rotating), &slog.HandlerOptions{
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
		_ = rotating.Close()
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
		file:   rotating,
	}, nil
}

// Close flushes and closes the rotating file sink.
func (s *Set) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	return s.file.Close()
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

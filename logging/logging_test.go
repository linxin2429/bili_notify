package logging

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestOpenWritesStructuredCategoriesToStdout(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	loggers, err := Open(Config{Level: "debug", Version: "1.2.3", RunID: "run-1", Stdout: &stdout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = loggers.Close() })

	loggers.System.Info("started", "event", "process.start")
	loggers.Audit.Info("changed", "event", "administrator.operation")
	require.NoError(t, loggers.Close())

	scanner := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	categories := make([]string, 0, 2)
	for scanner.Scan() {
		var record map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		categories = append(categories, record["category"].(string))
		assert.Equal(t, "bili-notify", record["service"])
		assert.Equal(t, "1.2.3", record["version"])
		assert.Equal(t, float64(1), record["log_schema"])
		assert.Equal(t, "run-1", record["run_id"])
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, []string{"system", "audit"}, categories)
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "debug", value: "debug"},
		{name: "info", value: "INFO"},
		{name: "warn", value: " warn "},
		{name: "error", value: "error"},
		{name: "invalid", value: "trace", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseLevel(tt.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSetApplyUpdatesLevel(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	loggers, err := Open(Config{Level: "debug", Version: "test", RunID: "run-1", Stdout: &stdout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = loggers.Close() })

	require.NoError(t, loggers.Apply("warn"))
	loggers.System.Info("hidden")
	loggers.System.Error("visible")
	assert.NotContains(t, stdout.String(), "hidden")
	assert.Contains(t, stdout.String(), "visible")
}

func TestOTelLogsCorrelateAndRedactSecrets(t *testing.T) {
	t.Parallel()
	exporter := new(memoryLogExporter)
	loggerProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	t.Cleanup(func() { require.NoError(t, loggerProvider.Shutdown(context.Background())) })
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })

	var stdout bytes.Buffer
	loggers, err := Open(Config{Level: "info", Version: "test", RunID: "run-1", Stdout: &stdout, Provider: loggerProvider})
	require.NoError(t, err)
	ctx, span := tracerProvider.Tracer("test").Start(t.Context(), "parent")
	loggers.System.InfoContext(ctx, "setup", "setup_code", "stdout-only-code")
	loggers.System.ErrorContext(ctx, "delivery failed", "webhook", "https://example.test/?key=secret-value", "error", errors.New("request failed: https://example.test/open-apis/bot/v2/hook/path-secret-value?access_token=secret-value"))
	spanContext := span.SpanContext()
	span.End()

	records := exporter.Records()
	require.Len(t, records, 2)
	tests := []struct {
		name        string
		recordIndex int
		key         string
		wantValue   string
	}{
		{name: "setup code", recordIndex: 0, key: "setup_code", wantValue: "[REDACTED]"},
		{name: "webhook", recordIndex: 1, key: "webhook", wantValue: "[REDACTED]"},
		{name: "error URL token", recordIndex: 1, key: "error", wantValue: "request failed: [REDACTED_URL]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			attributes := make(map[string]string)
			records[tt.recordIndex].WalkAttributes(func(value attribute.KeyValue) bool {
				attributes[string(value.Key)] = value.Value.AsString()
				return true
			})
			assert.Equal(t, tt.wantValue, attributes[tt.key])
			assert.Equal(t, spanContext.TraceID(), records[tt.recordIndex].TraceID())
			assert.Equal(t, spanContext.SpanID(), records[tt.recordIndex].SpanID())
		})
	}
	assert.Contains(t, stdout.String(), "stdout-only-code")
	assert.NotContains(t, stdout.String(), "secret-value")
	assert.NotContains(t, stdout.String(), "path-secret-value")
}

type memoryLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memoryLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (e *memoryLogExporter) Shutdown(context.Context) error   { return nil }
func (e *memoryLogExporter) ForceFlush(context.Context) error { return nil }

func (e *memoryLogExporter) Records() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]sdklog.Record, len(e.records))
	for i, record := range e.records {
		result[i] = record.Clone()
	}
	return result
}

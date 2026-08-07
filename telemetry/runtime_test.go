package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func TestNewConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		wantError   string
	}{
		{name: "disabled", environment: map[string]string{"OTEL_SDK_DISABLED": "true"}},
		{name: "invalid disabled value", environment: map[string]string{"OTEL_SDK_DISABLED": "sometimes"}, wantError: "invalid OTEL_SDK_DISABLED"},
		{name: "invalid common protocol", environment: map[string]string{"OTEL_EXPORTER_OTLP_PROTOCOL": "json"}, wantError: "invalid OTEL_EXPORTER_OTLP_PROTOCOL"},
		{name: "invalid signal protocol", environment: map[string]string{"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL": "json"}, wantError: "invalid OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{
				"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
				"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
			} {
				t.Setenv(key, "")
			}
			for key, value := range tt.environment {
				t.Setenv(key, value)
			}

			runtime, err := New(t.Context(), "test")
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Shutdown(context.Background())) })
			assert.NotEmpty(t, runtime.InstanceID)
			assert.NotNil(t, runtime.TracerProvider)
			assert.NotNil(t, runtime.MeterProvider)
			assert.NotNil(t, runtime.LoggerProvider)
			assert.NotNil(t, runtime.Propagator)
		})
	}
}

func TestSignalProtocolPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		common    string
		signal    string
		want      string
		wantError bool
	}{
		{name: "default", want: "http/protobuf"},
		{name: "common", common: "grpc", want: "grpc"},
		{name: "signal overrides common", common: "grpc", signal: "http/protobuf", want: "http/protobuf"},
		{name: "invalid override", common: "grpc", signal: "json", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tt.common)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", tt.signal)
			got, err := signalProtocol("TRACES")
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewResourceUsesSafeProcessAttributes(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	t.Setenv("OTEL_SERVICE_NAME", "bili-notify-test")

	got, err := newResource(t.Context(), "1.2.3", "instance-1")
	require.NoError(t, err)

	tests := []struct {
		name    string
		key     attribute.Key
		want    string
		present bool
	}{
		{name: "service name", key: semconv.ServiceNameKey, want: "bili-notify-test", present: true},
		{name: "service version", key: semconv.ServiceVersionKey, want: "1.2.3", present: true},
		{name: "service instance", key: semconv.ServiceInstanceIDKey, want: "instance-1", present: true},
		{name: "process id", key: semconv.ProcessPIDKey, present: true},
		{name: "runtime name", key: semconv.ProcessRuntimeNameKey, want: "go", present: true},
		{name: "command arguments", key: semconv.ProcessCommandArgsKey},
		{name: "process owner", key: semconv.ProcessOwnerKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := got.Set().Value(tt.key)
			assert.Equal(t, tt.present, ok)
			if tt.want != "" {
				require.True(t, ok)
				assert.Equal(t, tt.want, value.AsString())
			}
		})
	}
}

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

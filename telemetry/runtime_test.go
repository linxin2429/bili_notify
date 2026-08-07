package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func TestNewConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		config    Config
		wantError string
	}{
		{name: "disabled", config: Config{Disabled: true}},
		{name: "invalid common protocol", config: Config{Protocol: "json"}, wantError: "invalid BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL"},
		{name: "invalid signal protocol", config: Config{LogsProtocol: "json"}, wantError: "invalid BILI_NOTIFY_OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"},
		{name: "negative metric interval", config: Config{MetricExportInterval: -time.Second}, wantError: "metric export interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runtime, err := New(t.Context(), tt.config, "test")
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
			t.Parallel()
			got, err := signalProtocol(tt.common, tt.signal, "TRACES")
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseOTLPEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		want      otlpEndpoint
		wantError string
	}{
		{
			name:     "http base",
			endpoint: "http://otel-collector:4318",
			want:     otlpEndpoint{host: "otel-collector:4318", insecure: true},
		},
		{
			name:     "https base with prefix",
			endpoint: "https://collector.example.com:4318/otlp",
			want:     otlpEndpoint{host: "collector.example.com:4318", basePath: "/otlp", insecure: false},
		},
		{
			name:     "host without scheme defaults to https",
			endpoint: "collector.example.com:4318",
			want:     otlpEndpoint{host: "collector.example.com:4318", insecure: false},
		},
		{
			name:      "empty",
			endpoint:  "   ",
			wantError: "empty",
		},
		{
			name:      "missing host",
			endpoint:  "http:///v1/traces",
			wantError: "missing host",
		},
		{
			name:      "unsupported scheme",
			endpoint:  "ftp://collector:4318",
			wantError: "unsupported scheme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOTLPEndpoint(tt.endpoint)
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJoinOTLPPath(t *testing.T) {
	tests := []struct {
		name       string
		basePath   string
		signalPath string
		want       string
	}{
		{name: "empty base", basePath: "", signalPath: defaultOTLPTracesPath, want: "/v1/traces"},
		{name: "root base", basePath: "/", signalPath: defaultOTLPMetricsPath, want: "/v1/metrics"},
		{name: "prefix base", basePath: "/otlp", signalPath: defaultOTLPLogsPath, want: "/otlp/v1/logs"},
		{name: "trailing slash prefix", basePath: "/otlp/", signalPath: defaultOTLPTracesPath, want: "/otlp/v1/traces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, joinOTLPPath(tt.basePath, tt.signalPath))
		})
	}
}

func TestNewResourceUsesSafeProcessAttributes(t *testing.T) {
	t.Parallel()

	got, err := newResource(t.Context(), Config{ServiceName: "bili-notify-test", DeploymentEnvironment: "test"}, "1.2.3", "instance-1")
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
		{name: "deployment environment", key: semconv.DeploymentEnvironmentNameKey, want: "test", present: true},
		{name: "command arguments", key: semconv.ProcessCommandArgsKey},
		{name: "process owner", key: semconv.ProcessOwnerKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value, ok := got.Set().Value(tt.key)
			assert.Equal(t, tt.present, ok)
			if tt.want != "" {
				require.True(t, ok)
				assert.Equal(t, tt.want, value.AsString())
			}
		})
	}
}

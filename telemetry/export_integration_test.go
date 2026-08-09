package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	otelmetric "go.opentelemetry.io/otel/metric"
	collectlog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

type otlpRequest struct {
	path        string
	contentType string
	body        []byte
	err         error
}

func TestRuntimeExportsAllSignalsOverOTLPHTTP(t *testing.T) {
	requests := make(chan otlpRequest, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		requests <- otlpRequest{path: request.URL.Path, contentType: request.Header.Get("Content-Type"), body: body, err: err}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	runtime, err := New(t.Context(), Config{
		Endpoint: server.URL + "/tenant-a/otlp", Protocol: "http/protobuf",
		ServiceName: "bili-notify-contract", DeploymentEnvironment: "integration",
		MetricExportInterval: time.Hour,
	}, "9.8.7")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = runtime.Shutdown(ctx)
		cancel()
	})

	ctx, span := runtime.Tracer().Start(t.Context(), "contract.trace")
	counter, err := runtime.Meter().Int64Counter("contract.metric")
	require.NoError(t, err)
	counter.Add(ctx, 7, otelmetric.WithAttributes(attribute.String("contract.kind", "local")))
	var record otellog.Record
	record.SetTimestamp(time.Now())
	record.SetBody(attribute.StringValue("contract.log"))
	record.AddAttributes(attribute.String("contract.field", "correlated"))
	runtime.LoggerProvider.Logger("contract").Emit(ctx, record)
	span.End()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, runtime.Shutdown(shutdownCtx))

	wantPaths := map[string][]string{
		"/tenant-a/otlp/v1/traces":  {"bili-notify-contract", "9.8.7", "contract.trace"},
		"/tenant-a/otlp/v1/metrics": {"bili-notify-contract", "contract.metric", "contract.kind", "local"},
		"/tenant-a/otlp/v1/logs":    {"bili-notify-contract", "contract.log", "contract.field", "correlated"},
	}
	seen := make(map[string]otlpRequest, len(wantPaths))
	for len(seen) < len(wantPaths) {
		select {
		case request := <-requests:
			require.NoError(t, request.err)
			if _, tracked := wantPaths[request.path]; tracked {
				seen[request.path] = request
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for all OTLP signals", "received paths: %v", seen)
		}
	}
	for path, values := range wantPaths {
		request := seen[path]
		assert.Equal(t, "application/x-protobuf", request.contentType, path)
		assert.NotEmpty(t, request.body, path)
		for _, value := range values {
			assert.True(t, bytes.Contains(request.body, []byte(value)), "%s payload should contain %q", path, value)
		}
	}
}

func TestRuntimeUnavailableCollectorDoesNotBlockRecording(t *testing.T) {
	listener := newClosedHTTPServer(t)
	runtime, err := New(t.Context(), Config{
		Endpoint: listener, Protocol: "http/protobuf", MetricExportInterval: time.Hour,
	}, "test")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = runtime.Shutdown(ctx)
		cancel()
	})

	done := make(chan struct{})
	go func() {
		_, span := runtime.Tracer().Start(context.Background(), "offline.trace")
		span.End()
		counter, counterErr := runtime.Meter().Int64Counter("offline.metric")
		if counterErr == nil {
			counter.Add(context.Background(), 1)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "recording telemetry blocked on an unavailable collector")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	t.Cleanup(cancel)
	err = runtime.Shutdown(shutdownCtx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "connection refused"), "unexpected shutdown error: %v", err)
}

func TestRuntimeExportsAllSignalsOverOTLPGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan string, 32)
	server := grpc.NewServer()
	collecttrace.RegisterTraceServiceServer(server, &grpcTraceCollector{requests: requests})
	collectmetric.RegisterMetricsServiceServer(server, &grpcMetricCollector{requests: requests})
	collectlog.RegisterLogsServiceServer(server, &grpcLogCollector{requests: requests})
	t.Cleanup(server.Stop)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	runtime, err := New(t.Context(), Config{
		Endpoint: "http://" + listener.Addr().String(), Protocol: "grpc",
		ServiceName: "bili-notify-grpc-contract", MetricExportInterval: time.Hour,
	}, "grpc-test")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = runtime.Shutdown(ctx)
		cancel()
	})
	ctx, span := runtime.Tracer().Start(t.Context(), "grpc.trace")
	counter, err := runtime.Meter().Int64Counter("grpc.metric")
	require.NoError(t, err)
	counter.Add(ctx, 1)
	var record otellog.Record
	record.SetBody(attribute.StringValue("grpc.log"))
	runtime.LoggerProvider.Logger("grpc-contract").Emit(ctx, record)
	span.End()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, runtime.Shutdown(shutdownCtx))

	seen := map[string]string{}
	for len(seen) < 3 {
		select {
		case request := <-requests:
			kind, _, _ := strings.Cut(request, ":")
			seen[kind] = request
		case err := <-serveErrors:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for OTLP/gRPC signals", "received: %v", seen)
		}
	}
	assert.Contains(t, seen["traces"], "grpc.trace")
	assert.Contains(t, seen["metrics"], "grpc.metric")
	assert.Contains(t, seen["logs"], "grpc.log")
	for _, payload := range seen {
		assert.Contains(t, payload, "bili-notify-grpc-contract")
	}
}

type grpcTraceCollector struct {
	collecttrace.UnimplementedTraceServiceServer
	requests chan string
}

type grpcMetricCollector struct {
	collectmetric.UnimplementedMetricsServiceServer
	requests chan string
}

type grpcLogCollector struct {
	collectlog.UnimplementedLogsServiceServer
	requests chan string
}

func (c *grpcTraceCollector) Export(ctx context.Context, request *collecttrace.ExportTraceServiceRequest) (*collecttrace.ExportTraceServiceResponse, error) {
	c.requests <- "traces:" + request.String()
	return &collecttrace.ExportTraceServiceResponse{}, nil
}

func (c *grpcMetricCollector) Export(ctx context.Context, request *collectmetric.ExportMetricsServiceRequest) (*collectmetric.ExportMetricsServiceResponse, error) {
	c.requests <- "metrics:" + request.String()
	return &collectmetric.ExportMetricsServiceResponse{}, nil
}

func (c *grpcLogCollector) Export(ctx context.Context, request *collectlog.ExportLogsServiceRequest) (*collectlog.ExportLogsServiceResponse, error) {
	c.requests <- "logs:" + request.String()
	return &collectlog.ExportLogsServiceResponse{}, nil
}

func newClosedHTTPServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	return endpoint
}

func BenchmarkTelemetryRecordDisabled(b *testing.B) {
	runtime, err := New(context.Background(), Config{Disabled: true}, "benchmark")
	require.NoError(b, err)
	counter, err := runtime.Meter().Int64Counter("benchmark.notifications")
	require.NoError(b, err)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		counter.Add(ctx, 1)
	}
}

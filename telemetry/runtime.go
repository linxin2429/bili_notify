// Package telemetry owns the process-wide OpenTelemetry SDK lifecycle.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	otelmetric "go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
)

const (
	defaultOTLPTracesPath  = "/v1/traces"
	defaultOTLPMetricsPath = "/v1/metrics"
	defaultOTLPLogsPath    = "/v1/logs"
)

const instrumentationName = "github.com/linxin2429/bili_notify"

// Config contains the process-lifetime OpenTelemetry settings.
type Config struct {
	Disabled              bool
	ServiceName           string
	DeploymentEnvironment string
	Endpoint              string
	Protocol              string
	TracesProtocol        string
	MetricsProtocol       string
	LogsProtocol          string
	MetricExportInterval  time.Duration
}

// Runtime contains isolated providers shared by application components.
type Runtime struct {
	TracerProvider oteltrace.TracerProvider
	MeterProvider  otelmetric.MeterProvider
	LoggerProvider otellog.LoggerProvider
	Propagator     propagation.TextMapPropagator
	InstanceID     string
	shutdown       func(context.Context) error
}

// New creates providers configured by cfg.
func New(ctx context.Context, cfg Config, version string) (*Runtime, error) {
	cfg = cfg.normalized()
	instanceID, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("generating service instance id: %w", err)
	}
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	if cfg.Disabled {
		return &Runtime{
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			LoggerProvider: lognoop.NewLoggerProvider(),
			Propagator:     propagator,
			InstanceID:     instanceID,
			shutdown:       func(context.Context) error { return nil },
		}, nil
	}

	if cfg.MetricExportInterval < 0 {
		return nil, errors.New("OpenTelemetry metric export interval must not be negative")
	}
	res, err := newResource(ctx, cfg, version, instanceID)
	if err != nil {
		return nil, err
	}
	traceExporter, metricExporter, logExporter, err := newExporters(ctx, cfg)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(traceExporter),
	)
	readerOptions := make([]sdkmetric.PeriodicReaderOption, 0, 1)
	if cfg.MetricExportInterval > 0 {
		readerOptions = append(readerOptions, sdkmetric.WithInterval(cfg.MetricExportInterval))
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, readerOptions...)),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = errors.Join(lp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx), tp.Shutdown(shutdownCtx))
		return nil, fmt.Errorf("starting Go runtime metrics: %w", err)
	}

	return &Runtime{
		TracerProvider: tp,
		MeterProvider:  mp,
		LoggerProvider: lp,
		Propagator:     propagator,
		InstanceID:     instanceID,
		shutdown: func(shutdownCtx context.Context) error {
			return errors.Join(lp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx), tp.Shutdown(shutdownCtx))
		},
	}, nil
}

// Tracer returns the application tracer.
func (r *Runtime) Tracer() oteltrace.Tracer {
	return r.TracerProvider.Tracer(instrumentationName)
}

// Meter returns the application meter.
func (r *Runtime) Meter() otelmetric.Meter {
	return r.MeterProvider.Meter(instrumentationName)
}

// Shutdown flushes all telemetry signals.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.shutdown == nil {
		return nil
	}
	return r.shutdown(ctx)
}

func (c Config) normalized() Config {
	c.ServiceName = strings.TrimSpace(c.ServiceName)
	if c.ServiceName == "" {
		c.ServiceName = "bili-notify"
	}
	c.DeploymentEnvironment = strings.TrimSpace(c.DeploymentEnvironment)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Protocol = strings.TrimSpace(c.Protocol)
	c.TracesProtocol = strings.TrimSpace(c.TracesProtocol)
	c.MetricsProtocol = strings.TrimSpace(c.MetricsProtocol)
	c.LogsProtocol = strings.TrimSpace(c.LogsProtocol)
	return c
}

func newResource(ctx context.Context, cfg Config, version, instanceID string) (*resource.Resource, error) {
	base, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithProcessPID(),
		resource.WithProcessExecutableName(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithProcessRuntimeDescription(),
		resource.WithOS(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("detecting telemetry resource: %w", err)
	}
	attributes := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(version),
		semconv.ServiceInstanceID(instanceID),
		attribute.String("service.namespace", "bili-notify"),
	}
	if cfg.DeploymentEnvironment != "" {
		attributes = append(attributes, semconv.DeploymentEnvironmentNameKey.String(cfg.DeploymentEnvironment))
	}
	identity := resource.NewWithAttributes(semconv.SchemaURL, attributes...)
	merged, err := resource.Merge(base, identity)
	if err != nil {
		return nil, fmt.Errorf("merging telemetry resource: %w", err)
	}
	return merged, nil
}

type traceExporter interface {
	sdktrace.SpanExporter
}

type metricExporter interface {
	sdkmetric.Exporter
}

type logExporter interface {
	sdklog.Exporter
}

func newExporters(ctx context.Context, cfg Config) (traceExporter, metricExporter, logExporter, error) {
	traceProtocol, err := signalProtocol(cfg.Protocol, cfg.TracesProtocol, "TRACES")
	if err != nil {
		return nil, nil, nil, err
	}
	metricProtocol, err := signalProtocol(cfg.Protocol, cfg.MetricsProtocol, "METRICS")
	if err != nil {
		return nil, nil, nil, err
	}
	logProtocol, err := signalProtocol(cfg.Protocol, cfg.LogsProtocol, "LOGS")
	if err != nil {
		return nil, nil, nil, err
	}

	var traces traceExporter
	switch traceProtocol {
	case "grpc":
		options, optErr := grpcTraceOptions(cfg.Endpoint)
		if optErr != nil {
			return nil, nil, nil, optErr
		}
		traces, err = otlptracegrpc.New(ctx, options...)
	case "http/protobuf":
		options, optErr := httpTraceOptions(cfg.Endpoint)
		if optErr != nil {
			return nil, nil, nil, optErr
		}
		traces, err = otlptracehttp.New(ctx, options...)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}
	var metrics metricExporter
	switch metricProtocol {
	case "grpc":
		options, optErr := grpcMetricOptions(cfg.Endpoint)
		if optErr != nil {
			_ = traces.Shutdown(ctx)
			return nil, nil, nil, optErr
		}
		metrics, err = otlpmetricgrpc.New(ctx, options...)
	case "http/protobuf":
		options, optErr := httpMetricOptions(cfg.Endpoint)
		if optErr != nil {
			_ = traces.Shutdown(ctx)
			return nil, nil, nil, optErr
		}
		metrics, err = otlpmetrichttp.New(ctx, options...)
	}
	if err != nil {
		_ = traces.Shutdown(ctx)
		return nil, nil, nil, fmt.Errorf("creating OTLP metric exporter: %w", err)
	}
	var logs logExporter
	switch logProtocol {
	case "grpc":
		options, optErr := grpcLogOptions(cfg.Endpoint)
		if optErr != nil {
			_ = errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx))
			return nil, nil, nil, optErr
		}
		logs, err = otlploggrpc.New(ctx, options...)
	case "http/protobuf":
		options, optErr := httpLogOptions(cfg.Endpoint)
		if optErr != nil {
			_ = errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx))
			return nil, nil, nil, optErr
		}
		logs, err = otlploghttp.New(ctx, options...)
	}
	if err != nil {
		_ = errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx))
		return nil, nil, nil, fmt.Errorf("creating OTLP log exporter: %w", err)
	}
	return traces, metrics, logs, nil
}

func grpcTraceOptions(endpoint string) ([]otlptracegrpc.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(parsed.host)}
	if parsed.insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	return options, nil
}

func grpcMetricOptions(endpoint string) ([]otlpmetricgrpc.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(parsed.host)}
	if parsed.insecure {
		options = append(options, otlpmetricgrpc.WithInsecure())
	}
	return options, nil
}

func grpcLogOptions(endpoint string) ([]otlploggrpc.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlploggrpc.Option{otlploggrpc.WithEndpoint(parsed.host)}
	if parsed.insecure {
		options = append(options, otlploggrpc.WithInsecure())
	}
	return options, nil
}

func httpTraceOptions(endpoint string) ([]otlptracehttp.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(parsed.host),
		otlptracehttp.WithURLPath(joinOTLPPath(parsed.basePath, defaultOTLPTracesPath)),
	}
	if parsed.insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	return options, nil
}

func httpMetricOptions(endpoint string) ([]otlpmetrichttp.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(parsed.host),
		otlpmetrichttp.WithURLPath(joinOTLPPath(parsed.basePath, defaultOTLPMetricsPath)),
	}
	if parsed.insecure {
		options = append(options, otlpmetrichttp.WithInsecure())
	}
	return options, nil
}

func httpLogOptions(endpoint string) ([]otlploghttp.Option, error) {
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := parseOTLPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	options := []otlploghttp.Option{
		otlploghttp.WithEndpoint(parsed.host),
		otlploghttp.WithURLPath(joinOTLPPath(parsed.basePath, defaultOTLPLogsPath)),
	}
	if parsed.insecure {
		options = append(options, otlploghttp.WithInsecure())
	}
	return options, nil
}

type otlpEndpoint struct {
	host     string
	basePath string
	insecure bool
}

// parseOTLPEndpoint treats Endpoint as OTEL_EXPORTER_OTLP_ENDPOINT: a base URL
// whose HTTP signal paths are /v1/{traces,metrics,logs} relative to any path prefix.
// WithEndpointURL alone is wrong here because an empty path becomes "/" and does
// not append the default signal paths.
func parseOTLPEndpoint(endpoint string) (otlpEndpoint, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return otlpEndpoint{}, errors.New("OTLP endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return otlpEndpoint{}, fmt.Errorf("invalid OTLP endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return otlpEndpoint{}, fmt.Errorf("invalid OTLP endpoint %q: missing host", endpoint)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "unix":
	default:
		return otlpEndpoint{}, fmt.Errorf("invalid OTLP endpoint %q: unsupported scheme %q", endpoint, u.Scheme)
	}
	return otlpEndpoint{
		host:     u.Host,
		basePath: u.Path,
		insecure: scheme == "http" || scheme == "unix",
	}, nil
}

func joinOTLPPath(basePath, signalPath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" || basePath == "/" {
		return signalPath
	}
	joined := path.Join(basePath, signalPath)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

func signalProtocol(common, override, signal string) (string, error) {
	key := "BILI_NOTIFY_OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"
	protocol := override
	if protocol == "" {
		key = "BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL"
		protocol = common
	}
	if protocol == "" {
		return "http/protobuf", nil
	}
	if protocol != "grpc" && protocol != "http/protobuf" {
		return "", fmt.Errorf("invalid %s %q", key, protocol)
	}
	return protocol, nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

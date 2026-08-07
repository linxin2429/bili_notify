// Package telemetry owns the process-wide OpenTelemetry SDK lifecycle.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
		options := make([]otlptracegrpc.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
		}
		traces, err = otlptracegrpc.New(ctx, options...)
	case "http/protobuf":
		options := make([]otlptracehttp.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		}
		traces, err = otlptracehttp.New(ctx, options...)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}
	var metrics metricExporter
	switch metricProtocol {
	case "grpc":
		options := make([]otlpmetricgrpc.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlpmetricgrpc.WithEndpointURL(cfg.Endpoint))
		}
		metrics, err = otlpmetricgrpc.New(ctx, options...)
	case "http/protobuf":
		options := make([]otlpmetrichttp.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
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
		options := make([]otlploggrpc.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlploggrpc.WithEndpointURL(cfg.Endpoint))
		}
		logs, err = otlploggrpc.New(ctx, options...)
	case "http/protobuf":
		options := make([]otlploghttp.Option, 0, 1)
		if cfg.Endpoint != "" {
			options = append(options, otlploghttp.WithEndpointURL(cfg.Endpoint))
		}
		logs, err = otlploghttp.New(ctx, options...)
	}
	if err != nil {
		_ = errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx))
		return nil, nil, nil, fmt.Errorf("creating OTLP log exporter: %w", err)
	}
	return traces, metrics, logs, nil
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

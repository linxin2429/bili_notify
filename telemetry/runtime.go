// Package telemetry owns the process-wide OpenTelemetry SDK lifecycle.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
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

// Runtime contains isolated providers shared by application components.
type Runtime struct {
	TracerProvider oteltrace.TracerProvider
	MeterProvider  otelmetric.MeterProvider
	LoggerProvider otellog.LoggerProvider
	Propagator     propagation.TextMapPropagator
	InstanceID     string
	shutdown       func(context.Context) error
}

// New creates providers configured by the standard OTEL_* environment variables.
func New(ctx context.Context, version string) (*Runtime, error) {
	disabled, err := sdkDisabled()
	if err != nil {
		return nil, err
	}
	instanceID, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("generating service instance id: %w", err)
	}
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	if disabled {
		return &Runtime{
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			LoggerProvider: lognoop.NewLoggerProvider(),
			Propagator:     propagator,
			InstanceID:     instanceID,
			shutdown:       func(context.Context) error { return nil },
		}, nil
	}

	res, err := newResource(ctx, version, instanceID)
	if err != nil {
		return nil, err
	}
	traceExporter, metricExporter, logExporter, err := newExporters(ctx)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(traceExporter),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
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

func sdkDisabled() (bool, error) {
	value := strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED"))
	if value == "" {
		return false, nil
	}
	disabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid OTEL_SDK_DISABLED %q: %w", value, err)
	}
	return disabled, nil
}

func newResource(ctx context.Context, version, instanceID string) (*resource.Resource, error) {
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = "bili-notify"
	}
	base, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("detecting telemetry resource: %w", err)
	}
	identity := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
		semconv.ServiceInstanceID(instanceID),
		attribute.String("service.namespace", "bili-notify"),
	)
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

func newExporters(ctx context.Context) (traceExporter, metricExporter, logExporter, error) {
	traceProtocol, err := signalProtocol("TRACES")
	if err != nil {
		return nil, nil, nil, err
	}
	metricProtocol, err := signalProtocol("METRICS")
	if err != nil {
		return nil, nil, nil, err
	}
	logProtocol, err := signalProtocol("LOGS")
	if err != nil {
		return nil, nil, nil, err
	}

	var traces traceExporter
	switch traceProtocol {
	case "grpc":
		traces, err = otlptracegrpc.New(ctx)
	case "http/protobuf":
		traces, err = otlptracehttp.New(ctx)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}
	var metrics metricExporter
	switch metricProtocol {
	case "grpc":
		metrics, err = otlpmetricgrpc.New(ctx)
	case "http/protobuf":
		metrics, err = otlpmetrichttp.New(ctx)
	}
	if err != nil {
		_ = traces.Shutdown(ctx)
		return nil, nil, nil, fmt.Errorf("creating OTLP metric exporter: %w", err)
	}
	var logs logExporter
	switch logProtocol {
	case "grpc":
		logs, err = otlploggrpc.New(ctx)
	case "http/protobuf":
		logs, err = otlploghttp.New(ctx)
	}
	if err != nil {
		_ = errors.Join(metrics.Shutdown(ctx), traces.Shutdown(ctx))
		return nil, nil, nil, fmt.Errorf("creating OTLP log exporter: %w", err)
	}
	return traces, metrics, logs, nil
}

func signalProtocol(signal string) (string, error) {
	key := "OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"
	protocol := strings.TrimSpace(os.Getenv(key))
	if protocol == "" {
		key = "OTEL_EXPORTER_OTLP_PROTOCOL"
		protocol = strings.TrimSpace(os.Getenv(key))
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

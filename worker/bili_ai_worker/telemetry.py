from __future__ import annotations

import logging
import os
import uuid
from dataclasses import dataclass
from urllib.parse import urlsplit, urlunsplit

from opentelemetry import metrics, trace
from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

from bili_ai_worker import __version__


def _bool_env(name: str, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    normalized = value.strip().lower()
    if normalized in {"1", "true", "yes", "on"}:
        return True
    if normalized in {"0", "false", "no", "off"}:
        return False
    raise ValueError(f"invalid boolean {name}={value!r}")


def _signal_endpoint(base: str, signal: str) -> str:
    parsed = urlsplit(base.strip())
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValueError("BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute HTTP(S) URL")
    path = parsed.path.rstrip("/") + f"/v1/{signal}"
    return urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


@dataclass
class WorkerTelemetry:
    tracer_provider: TracerProvider | None = None
    meter_provider: MeterProvider | None = None
    logger_provider: LoggerProvider | None = None
    logging_handler: logging.Handler | None = None
    httpx_instrumented: bool = False

    def shutdown(self) -> None:
        if self.httpx_instrumented:
            HTTPXClientInstrumentor().uninstrument()
        if self.logger_provider is not None:
            self.logger_provider.shutdown()
        if self.meter_provider is not None:
            self.meter_provider.shutdown()
        if self.tracer_provider is not None:
            self.tracer_provider.shutdown()


def configure_telemetry() -> WorkerTelemetry:
    if _bool_env("BILI_NOTIFY_OTEL_SDK_DISABLED", True):
        return WorkerTelemetry()
    endpoint = os.environ.get("BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT", "").strip()
    protocol = os.environ.get("BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf").strip()
    if protocol != "http/protobuf":
        raise ValueError("AI worker currently requires BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf")
    try:
        interval = float(os.environ.get("BILI_NOTIFY_OTEL_METRIC_EXPORT_INTERVAL_SEC", "15"))
    except ValueError as exc:
        raise ValueError("BILI_NOTIFY_OTEL_METRIC_EXPORT_INTERVAL_SEC must be a positive number") from exc
    if interval <= 0:
        raise ValueError("BILI_NOTIFY_OTEL_METRIC_EXPORT_INTERVAL_SEC must be a positive number")
    service_name = os.environ.get("BILI_NOTIFY_OTEL_SERVICE_NAME", "bili-notify-ai-worker").strip()
    environment = os.environ.get("BILI_NOTIFY_OTEL_DEPLOYMENT_ENVIRONMENT", "").strip()
    attributes: dict[str, str] = {
        "service.name": service_name or "bili-notify-ai-worker",
        "service.namespace": "bili-notify",
        "service.version": __version__,
        "service.instance.id": uuid.uuid4().hex,
    }
    if environment:
        attributes["deployment.environment.name"] = environment
    resource = Resource.create(attributes)

    tracer_provider = TracerProvider(resource=resource)
    tracer_provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=_signal_endpoint(endpoint, "traces"))))
    trace.set_tracer_provider(tracer_provider)

    reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=_signal_endpoint(endpoint, "metrics")), export_interval_millis=interval * 1000
    )
    meter_provider = MeterProvider(resource=resource, metric_readers=[reader])
    metrics.set_meter_provider(meter_provider)

    logger_provider = LoggerProvider(resource=resource)
    logger_provider.add_log_record_processor(
        BatchLogRecordProcessor(OTLPLogExporter(endpoint=_signal_endpoint(endpoint, "logs")))
    )
    logging_handler = LoggingHandler(logger_provider=logger_provider)
    HTTPXClientInstrumentor().instrument(tracer_provider=tracer_provider, meter_provider=meter_provider)
    return WorkerTelemetry(tracer_provider, meter_provider, logger_provider, logging_handler, True)


tracer = trace.get_tracer("github.com/linxin2429/bili_notify/worker")
meter = metrics.get_meter("github.com/linxin2429/bili_notify/worker")
jobs = meter.create_counter("bili_notify.ai_worker.jobs", unit="{job}")
job_duration = meter.create_histogram("bili_notify.ai_worker.job.duration", unit="s")
provider_requests = meter.create_counter("bili_notify.ai_worker.provider.requests", unit="{request}")
provider_duration = meter.create_histogram("bili_notify.ai_worker.provider.duration", unit="s")
audio_bytes = meter.create_counter("bili_notify.ai_worker.audio.bytes", unit="By")
cache_bytes = meter.create_gauge("bili_notify.ai_worker.cache.bytes", unit="By")

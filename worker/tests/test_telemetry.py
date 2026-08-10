from __future__ import annotations

import pytest

from bili_ai_worker import telemetry


def test_telemetry_is_disabled_by_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("BILI_NOTIFY_OTEL_SDK_DISABLED", raising=False)
    runtime = telemetry.configure_telemetry()
    assert runtime.tracer_provider is None
    assert runtime.meter_provider is None
    assert runtime.logger_provider is None


@pytest.mark.parametrize(
    ("base", "signal", "expected"),
    [
        ("http://collector:4318", "traces", "http://collector:4318/v1/traces"),
        ("https://collector.example/otlp/", "logs", "https://collector.example/otlp/v1/logs"),
    ],
)
def test_signal_endpoint(base: str, signal: str, expected: str) -> None:
    assert telemetry._signal_endpoint(base, signal) == expected


@pytest.mark.parametrize("protocol", ["grpc", "json", ""])
def test_enabled_telemetry_rejects_unsupported_protocol(
    protocol: str, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("BILI_NOTIFY_OTEL_SDK_DISABLED", "false")
    monkeypatch.setenv("BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
    monkeypatch.setenv("BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL", protocol)
    with pytest.raises(ValueError, match="http/protobuf"):
        telemetry.configure_telemetry()


@pytest.mark.parametrize("interval", ["0", "-1", "invalid"])
def test_enabled_telemetry_rejects_invalid_metric_interval(
    interval: str, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("BILI_NOTIFY_OTEL_SDK_DISABLED", "false")
    monkeypatch.setenv("BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
    monkeypatch.setenv("BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
    monkeypatch.setenv("BILI_NOTIFY_OTEL_METRIC_EXPORT_INTERVAL_SEC", interval)
    with pytest.raises(ValueError, match="positive number"):
        telemetry.configure_telemetry()

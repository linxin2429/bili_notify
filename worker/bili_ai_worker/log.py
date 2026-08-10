from __future__ import annotations

import json
import logging
import os
import sys
from datetime import UTC, datetime
from typing import Any

from opentelemetry import trace

_STANDARD_RECORD_FIELDS = frozenset(logging.makeLogRecord({}).__dict__)


class JSONFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "time": datetime.fromtimestamp(record.created, UTC).isoformat(timespec="milliseconds"),
            "level": record.levelname.lower(),
            "component": "ai-worker",
            "message": record.getMessage(),
        }
        for key, value in record.__dict__.items():
            if key not in _STANDARD_RECORD_FIELDS and key not in {"message", "asctime"}:
                payload[key] = value
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        span = trace.get_current_span().get_span_context()
        if span.is_valid:
            payload["trace_id"] = format(span.trace_id, "032x")
            payload["span_id"] = format(span.span_id, "016x")
        return json.dumps(payload, ensure_ascii=False, default=str, separators=(",", ":"))


def configure_logging(otel_handler: logging.Handler | None = None) -> None:
    raw_level = os.environ.get("BILI_NOTIFY_AI_LOG_LEVEL", os.environ.get("BILI_NOTIFY_LOG_LEVEL", "info"))
    level = getattr(logging, raw_level.strip().upper(), None)
    if not isinstance(level, int):
        raise ValueError(f"invalid AI worker log level {raw_level!r}")  # noqa: TRY004

    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JSONFormatter())
    root = logging.getLogger()
    root.handlers.clear()
    root.addHandler(handler)
    if otel_handler is not None:
        root.addHandler(otel_handler)
    root.setLevel(level)

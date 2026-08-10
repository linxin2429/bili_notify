import json
import logging

from bili_ai_worker.log import JSONFormatter


def test_json_formatter_preserves_structured_fields() -> None:
    record = logging.makeLogRecord(
        {
            "levelno": logging.INFO,
            "levelname": "INFO",
            "msg": "request completed",
            "event": "provider.transcription.response",
            "audio_bytes": 19_297_788,
        }
    )

    payload = json.loads(JSONFormatter().format(record))

    assert payload["level"] == "info"
    assert payload["component"] == "ai-worker"
    assert payload["message"] == "request completed"
    assert payload["event"] == "provider.transcription.response"
    assert payload["audio_bytes"] == 19_297_788

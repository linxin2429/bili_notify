from __future__ import annotations

import asyncio
import json
import logging
import math
import struct
import time
import wave
from io import BytesIO
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx

from bili_ai_worker.media import audio_duration_seconds
from bili_ai_worker.telemetry import provider_duration, provider_requests

logger = logging.getLogger("bili_ai_worker.provider")

TRANSCRIBE_ATTEMPTS = 4
_RETRYABLE_CODES = frozenset(
    {"provider_timeout", "provider_unreachable", "provider_rate_limited", "provider_failure", "timestamps_unsupported"}
)
_NON_RETRYABLE_STATUS = frozenset({401, 403, 404, 413})


class ProviderError(RuntimeError):
    def __init__(
        self, code: str, message: str, *, status_code: int = 0, provider_error: str = ""
    ) -> None:
        super().__init__(message)
        self.code = code
        self.status_code = status_code
        self.provider_error = provider_error


def _error_message(response: httpx.Response) -> str:
    try:
        payload = response.json()
    except json.JSONDecodeError:
        return ""
    if not isinstance(payload, dict):
        return ""
    parts: list[str] = []
    error = payload.get("error")
    if isinstance(error, dict):
        if isinstance(error.get("message"), str) and error["message"].strip():
            parts.append(error["message"].strip())
        metadata = error.get("metadata")
        if isinstance(metadata, dict):
            for key in ("provider_name", "raw"):
                value = metadata.get(key)
                if isinstance(value, str) and value.strip() and value.strip() not in parts:
                    parts.append(value.strip())
    if not parts:
        for key in ("message", "detail"):
            if isinstance(payload.get(key), str) and payload[key].strip():
                parts.append(payload[key].strip())
                break
    return " ".join(parts)


def _raise_for_status(response: httpx.Response, label: str) -> None:
    if 200 <= response.status_code < 300:
        return
    provider_error = _error_message(response)
    if response.status_code in (401, 403):
        code, message = "provider_authentication", f"{label} provider rejected the API key"
    elif response.status_code == 404:
        code, message = "provider_model_not_found", f"{label} provider endpoint or model was not found"
    elif response.status_code == 413:
        code, message = "provider_payload_too_large", f"{label} provider returned HTTP 413 (payload too large)"
    elif response.status_code == 429:
        code, message = "provider_rate_limited", f"{label} provider rate limited the request"
    else:
        code, message = "provider_failure", f"{label} provider returned HTTP {response.status_code}"
    raise ProviderError(code, message, status_code=response.status_code, provider_error=provider_error)


def _timeout(config: Any, *, probe: bool = False) -> httpx.Timeout:
    total = min(float(config.timeout_sec), 20.0) if probe else float(config.timeout_sec)
    return httpx.Timeout(total, connect=min(15.0, total))


async def transcribe(
    path: Path, config: Any, *, job_id: str = ""
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    try:
        return await _transcribe_with_retries(path, config, job_id=job_id, verbose=True)
    except ProviderError as exc:
        if not _should_fallback_to_json(exc):
            raise
        logger.warning(
            "verbose transcription failed; retrying without segment timestamps",
            extra={
                "event": "provider.transcription.fallback_json",
                "job_id": job_id,
                "error_code": exc.code,
                "http_status": exc.status_code,
            },
        )
        return await _transcribe_with_retries(path, config, job_id=job_id, verbose=False)


async def _transcribe_with_retries(
    path: Path, config: Any, *, job_id: str, verbose: bool
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    last_error: ProviderError | None = None
    for attempt in range(1, TRANSCRIBE_ATTEMPTS + 1):
        try:
            return await _transcribe_once(path, config, job_id=job_id, verbose=verbose, attempt=attempt)
        except ProviderError as exc:
            last_error = exc
            if not _retryable(exc) or attempt >= TRANSCRIBE_ATTEMPTS:
                raise
            logger.warning(
                "retrying transcription request",
                extra={
                    "event": "provider.transcription.retry",
                    "job_id": job_id,
                    "attempt": attempt,
                    "error_code": exc.code,
                    "http_status": exc.status_code,
                    "response_format": "verbose_json" if verbose else "json",
                },
            )
            await _sleep(_transcribe_retry_delay(attempt))
    assert last_error is not None
    raise last_error


async def _transcribe_once(
    path: Path, config: Any, *, job_id: str, verbose: bool, attempt: int
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    fields = _transcription_fields(config, verbose=verbose)
    headers = {"Authorization": f"Bearer {config.api_key}"}
    timeout = _timeout(config)
    endpoint = f"{config.base_url.rstrip('/')}/audio/transcriptions"
    audio_bytes = path.stat().st_size
    started = time.monotonic()
    request_fields = {
        "event": "provider.transcription.request",
        "job_id": job_id,
        "provider_origin": _origin(endpoint),
        "model": config.model,
        "audio_bytes": audio_bytes,
        "audio_format": path.suffix.removeprefix(".").lower() or "wav",
        "timeout_sec": config.timeout_sec,
        "attempt": attempt,
        "response_format": fields["response_format"],
    }
    logger.info("sending transcription request", extra=request_fields)
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=False) as client:
            with path.open("rb") as audio:
                response = await client.post(
                    endpoint,
                    headers=headers,
                    data=fields,
                    files={"file": (path.name, audio, _audio_content_type(path))},
                )
    except httpx.TimeoutException as exc:
        _log_transport_failure("transcription", "timeout", request_fields, started, exc)
        raise ProviderError("provider_timeout", "transcription provider timed out", provider_error=str(exc)) from exc
    except httpx.HTTPError as exc:
        _log_transport_failure("transcription", "unreachable", request_fields, started, exc)
        raise ProviderError(
            "provider_unreachable", "transcription provider is unreachable", provider_error=str(exc)
        ) from exc
    _log_response("transcription", response, request_fields, started, config.api_key)
    if response.status_code == 413:
        raise ProviderError(
            "provider_payload_too_large",
            f"transcription provider rejected a {audio_bytes}-byte audio chunk with HTTP 413 (payload too large)",
            status_code=response.status_code,
            provider_error=_error_message(response),
        )
    _raise_for_status(response, "transcription")
    try:
        payload = response.json()
    except json.JSONDecodeError as exc:
        raise ProviderError("provider_invalid_response", "transcription provider returned invalid JSON") from exc
    return _transcription_result(path, payload, verbose=verbose)


def _transcription_fields(config: Any, *, verbose: bool) -> dict[str, str]:
    fields = {
        "model": config.model,
        "response_format": "verbose_json" if verbose else "json",
    }
    if verbose:
        fields["timestamp_granularities[]"] = "segment"
    if config.language:
        fields["language"] = config.language
    if config.prompt:
        fields["prompt"] = config.prompt
    return fields


def _audio_content_type(path: Path) -> str:
    suffix = path.suffix.removeprefix(".").lower()
    if suffix == "wav":
        return "audio/wav"
    if suffix == "mp3":
        return "audio/mpeg"
    if suffix == "flac":
        return "audio/flac"
    return f"audio/{suffix}" if suffix else "audio/wav"


def _retryable(error: ProviderError) -> bool:
    if error.status_code in _NON_RETRYABLE_STATUS:
        return False
    if error.code in {"provider_authentication", "provider_model_not_found", "provider_payload_too_large"}:
        return False
    if error.status_code in {400, 408, 429} or error.status_code >= 500:
        return True
    return error.code in _RETRYABLE_CODES


def _should_fallback_to_json(error: ProviderError) -> bool:
    if error.status_code in _NON_RETRYABLE_STATUS:
        return False
    if error.code in {"timestamps_unsupported", "provider_invalid_response"}:
        return True
    return error.status_code == 400


def _transcribe_retry_delay(attempt: int) -> float:
    return float(min(2 ** max(attempt - 1, 0), 8))


async def _sleep(seconds: float) -> None:
    await asyncio.sleep(seconds)


def _usage(payload: dict[str, Any]) -> dict[str, Any]:
    usage = payload.get("usage")
    return usage if isinstance(usage, dict) else {}


def _normalize_segments(segments: list[Any]) -> list[dict[str, Any]]:
    normalized: list[dict[str, Any]] = []
    for segment in segments:
        try:
            start = float(segment["start"])
            end = float(segment["end"])
            text = str(segment["text"]).strip()
        except (KeyError, TypeError, ValueError) as exc:
            raise ProviderError("provider_invalid_response", "transcription segment is malformed") from exc
        if text and end >= start >= 0:
            normalized.append({"start": start, "end": end, "text": text})
    if not normalized:
        raise ProviderError("provider_invalid_response", "transcription response has no usable segments")
    return normalized


def _payload_duration_seconds(payload: dict[str, Any], path: Path) -> float:
    duration = payload.get("duration")
    if isinstance(duration, (int, float)) and duration > 0:
        return float(duration)
    usage = _usage(payload)
    seconds = usage.get("seconds")
    if isinstance(seconds, (int, float)) and seconds > 0:
        return float(seconds)
    return audio_duration_seconds(path)


def _transcription_result(path: Path, payload: Any, *, verbose: bool) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    if not isinstance(payload, dict):
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response")
    usage = _usage(payload)
    segments = payload.get("segments")
    if isinstance(segments, list) and segments:
        return _normalize_segments(segments), usage
    if verbose:
        raise ProviderError("timestamps_unsupported", "transcription provider did not return segment timestamps")
    text = str(payload.get("text") or "").strip()
    if not text:
        raise ProviderError("provider_invalid_response", "transcription provider returned an empty transcript")
    duration = max(_payload_duration_seconds(payload, path), 0.001)
    return [{"start": 0.0, "end": duration, "text": text}], usage


async def complete(
    messages: list[dict[str, str]], config: Any, *, job_id: str = ""
) -> tuple[str, dict[str, Any]]:
    body: dict[str, Any] = {
        "model": config.model,
        "messages": messages,
        "temperature": config.temperature,
    }
    if config.max_output_tokens > 0:
        body["max_tokens"] = config.max_output_tokens
    headers = {"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"}
    timeout = _timeout(config)
    endpoint = f"{config.base_url.rstrip('/')}/chat/completions"
    started = time.monotonic()
    request_fields = {
        "event": "provider.text.request",
        "job_id": job_id,
        "provider_origin": _origin(endpoint),
        "model": config.model,
        "message_count": len(messages),
        "input_chars": sum(len(message.get("content", "")) for message in messages),
        "timeout_sec": config.timeout_sec,
    }
    logger.info("sending text completion request", extra=request_fields)
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=False) as client:
            response = await client.post(endpoint, headers=headers, json=body)
    except httpx.TimeoutException as exc:
        _log_transport_failure("text", "timeout", request_fields, started, exc)
        raise ProviderError("provider_timeout", "text provider timed out", provider_error=str(exc)) from exc
    except httpx.HTTPError as exc:
        _log_transport_failure("text", "unreachable", request_fields, started, exc)
        raise ProviderError("provider_unreachable", "text provider is unreachable", provider_error=str(exc)) from exc
    _log_response("text", response, request_fields, started, config.api_key)
    _raise_for_status(response, "text")
    try:
        payload = response.json()
        text = str(payload["choices"][0]["message"]["content"]).strip()
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
        raise ProviderError("provider_invalid_response", "text provider returned an invalid response") from exc
    if not text:
        raise ProviderError("provider_invalid_response", "text provider returned an empty summary")
    usage = payload.get("usage") if isinstance(payload.get("usage"), dict) else {}
    return text, usage


async def test_provider(kind: str, config: Any) -> int:
    if kind == "text":
        return await _test_text(config)
    if kind == "transcription":
        return await _test_transcription(config)
    raise ProviderError("invalid_profile_kind", f"unsupported profile kind {kind!r}")


async def _test_text(config: Any) -> int:
    body = {
        "model": config.model,
        "messages": [{"role": "user", "content": "Reply with OK."}],
        "temperature": config.temperature,
        "max_tokens": 8,
    }
    headers = {"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"}
    try:
        async with httpx.AsyncClient(timeout=_timeout(config, probe=True), follow_redirects=False) as client:
            response = await client.post(
                f"{config.base_url.rstrip('/')}/chat/completions", headers=headers, json=body
            )
    except httpx.TimeoutException as exc:
        raise ProviderError("provider_timeout", "text provider timed out", provider_error=str(exc)) from exc
    except httpx.HTTPError as exc:
        raise ProviderError("provider_unreachable", "text provider is unreachable", provider_error=str(exc)) from exc
    _raise_for_status(response, "text")
    try:
        text = str(response.json()["choices"][0]["message"]["content"]).strip()
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
        raise ProviderError("provider_invalid_response", "text provider returned an invalid response") from exc
    if not text:
        raise ProviderError("provider_invalid_response", "text provider returned an empty response")
    return response.status_code


async def _test_transcription(config: Any) -> int:
    fields = {
        "model": config.model,
        "response_format": "verbose_json",
        "timestamp_granularities[]": "segment",
    }
    if config.language:
        fields["language"] = config.language
    if config.prompt:
        fields["prompt"] = config.prompt
    headers = {"Authorization": f"Bearer {config.api_key}"}
    try:
        async with httpx.AsyncClient(timeout=_timeout(config, probe=True), follow_redirects=False) as client:
            response = await client.post(
                f"{config.base_url.rstrip('/')}/audio/transcriptions",
                headers=headers,
                data=fields,
                files={"file": ("connectivity-test.wav", _probe_wav(), "audio/wav")},
            )
    except httpx.TimeoutException as exc:
        raise ProviderError(
            "provider_timeout", "transcription provider timed out", provider_error=str(exc)
        ) from exc
    except httpx.HTTPError as exc:
        raise ProviderError(
            "provider_unreachable", "transcription provider is unreachable", provider_error=str(exc)
        ) from exc
    _raise_for_status(response, "transcription")
    try:
        payload = response.json()
    except json.JSONDecodeError as exc:
        raise ProviderError("provider_invalid_response", "transcription provider returned invalid JSON") from exc
    if not isinstance(payload, dict):
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response")
    return response.status_code


def _probe_wav() -> bytes:
    sample_rate = 8_000
    duration_seconds = 0.25
    frames = bytearray()
    for index in range(round(sample_rate * duration_seconds)):
        sample = round(2_000 * math.sin(2 * math.pi * 440 * index / sample_rate))
        frames.extend(struct.pack("<h", sample))
    output = BytesIO()
    with wave.open(output, "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(sample_rate)
        audio.writeframes(frames)
    return output.getvalue()


def _origin(url: str) -> str:
    parsed = urlsplit(url)
    return f"{parsed.scheme}://{parsed.netloc}"


def _elapsed_ms(started: float) -> int:
    return round((time.monotonic() - started) * 1000)


def _request_id(response: httpx.Response) -> str:
    for name in ("x-generation-id", "x-request-id", "request-id", "cf-ray"):
        if value := response.headers.get(name):
            return value[:200]
    return ""


def _redacted_error_message(response: httpx.Response, api_key: str) -> str:
    detail = " ".join(_error_message(response).split())
    if api_key:
        detail = detail.replace(api_key, "[REDACTED]")
    return detail[:500]


def _log_response(
    label: str,
    response: httpx.Response,
    request_fields: dict[str, Any],
    started: float,
    api_key: str,
) -> None:
    attributes = {"provider.operation": label, "http.response.status_code": response.status_code}
    provider_requests.add(1, attributes)
    provider_duration.record(time.monotonic() - started, attributes)
    response_fields = {
        **request_fields,
        "event": f"provider.{label}.response",
        "http_status": response.status_code,
        "duration_ms": _elapsed_ms(started),
        "response_bytes": len(response.content),
        "provider_request_id": _request_id(response),
    }
    if 200 <= response.status_code < 300:
        logger.info(f"{label} provider accepted request", extra=response_fields)
        return
    logger.warning(
        f"{label} provider rejected request",
        extra={**response_fields, "provider_error": _redacted_error_message(response, api_key)},
    )


def _log_transport_failure(
    label: str,
    failure: str,
    request_fields: dict[str, Any],
    started: float,
    error: httpx.HTTPError,
) -> None:
    attributes = {"provider.operation": label, "result": failure}
    provider_requests.add(1, attributes)
    provider_duration.record(time.monotonic() - started, attributes)
    logger.warning(
        f"{label} provider request failed",
        extra={
            **request_fields,
            "event": f"provider.{label}.{failure}",
            "duration_ms": _elapsed_ms(started),
            "error_type": type(error).__name__,
        },
    )

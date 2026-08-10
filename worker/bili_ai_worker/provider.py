from __future__ import annotations

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

logger = logging.getLogger("bili_ai_worker.provider")


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
    error = payload.get("error")
    if isinstance(error, dict) and isinstance(error.get("message"), str):
        return error["message"]
    for key in ("message", "detail"):
        if isinstance(payload.get(key), str):
            return payload[key]
    return ""


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
        "audio_format": path.suffix.removeprefix(".").lower(),
        "timeout_sec": config.timeout_sec,
    }
    logger.info("sending transcription request", extra=request_fields)
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=False) as client:
            with path.open("rb") as audio:
                response = await client.post(
                    endpoint,
                    headers=headers,
                    data=fields,
                    files={"file": (path.name, audio, "audio/flac")},
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
    segments = payload.get("segments")
    if not isinstance(segments, list) or not segments:
        raise ProviderError("timestamps_unsupported", "transcription provider did not return segment timestamps")
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
    usage = payload.get("usage") if isinstance(payload.get("usage"), dict) else {}
    return normalized, usage


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
    logger.warning(
        f"{label} provider request failed",
        extra={
            **request_fields,
            "event": f"provider.{label}.{failure}",
            "duration_ms": _elapsed_ms(started),
            "error_type": type(error).__name__,
        },
    )

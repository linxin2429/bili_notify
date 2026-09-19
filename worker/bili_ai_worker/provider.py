from __future__ import annotations

import asyncio
import base64
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

from bili_ai_worker.media import CHUNK_DURATION_MS, audio_duration_seconds
from bili_ai_worker.telemetry import provider_duration, provider_requests

logger = logging.getLogger("bili_ai_worker.provider")

TRANSCRIBE_ATTEMPTS = 4
PROTOCOL_OPENAI_AUDIO = "openai_audio_transcriptions"
PROTOCOL_GEMINI = "gemini_interactions"
PROTOCOL_CHAT_AUDIO = "openai_chat_audio"
CHAT_AUDIO_CHUNK_DURATION_MS = 150_000
GEMINI_WORD_GAP_SEC = 0.6
DEFAULT_CHAT_AUDIO_PROMPT = "请逐字转写这段音频中的语音为原文，不要总结或解释。若无明显语音，输出空。"
PROBE_CHAT_AUDIO_PROMPT = "若音频中有语音，请逐字转写；若没有可识别语音，请只回复 OK。"
_GEMINI_HOST = "generativelanguage.googleapis.com"
_GEMINI_LANGUAGES = {
    "zh": "cmn-Hans-CN",
    "zh-cn": "cmn-Hans-CN",
    "zh-hans": "cmn-Hans-CN",
    "cmn-hans-cn": "cmn-Hans-CN",
    "en": "en-US",
    "en-us": "en-US",
    "en-gb": "en-GB",
}
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


def transcription_protocol(base_url: str) -> str:
    parsed = urlsplit(base_url.strip())
    host = (parsed.hostname or "").lower()
    path = parsed.path.lower()
    if host == _GEMINI_HOST:
        return PROTOCOL_GEMINI
    if "dashscope" in host:
        return PROTOCOL_CHAT_AUDIO
    if host.endswith(".aliyuncs.com") and "compatible-mode" in path:
        return PROTOCOL_CHAT_AUDIO
    return PROTOCOL_OPENAI_AUDIO


def transcription_chunk_duration_ms(base_url: str) -> int:
    if transcription_protocol(base_url) == PROTOCOL_CHAT_AUDIO:
        return CHAT_AUDIO_CHUNK_DURATION_MS
    return CHUNK_DURATION_MS


async def transcribe(
    path: Path, config: Any, *, job_id: str = ""
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    protocol = transcription_protocol(config.base_url)
    if protocol == PROTOCOL_GEMINI:
        return await _transcribe_gemini(path, config, job_id=job_id)
    if protocol == PROTOCOL_CHAT_AUDIO:
        return await _transcribe_chat_audio(path, config, job_id=job_id)
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
        "transcription_protocol": PROTOCOL_OPENAI_AUDIO,
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
        protocol = transcription_protocol(config.base_url)
        if protocol == PROTOCOL_GEMINI:
            return await _test_gemini(config)
        if protocol == PROTOCOL_CHAT_AUDIO:
            return await _test_chat_audio(config)
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


async def _transcribe_gemini(
    path: Path, config: Any, *, job_id: str
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    try:
        return await _retry_transcription(
            lambda attempt: _transcribe_gemini_once(path, config, job_id=job_id, timestamps=True, attempt=attempt),
            job_id=job_id,
            protocol=PROTOCOL_GEMINI,
        )
    except ProviderError as exc:
        if not _should_fallback_to_json(exc):
            raise
        logger.warning(
            "verbose transcription failed; retrying without segment timestamps",
            extra={
                "event": "provider.transcription.fallback_json",
                "job_id": job_id,
                "transcription_protocol": PROTOCOL_GEMINI,
                "error_code": exc.code,
                "http_status": exc.status_code,
            },
        )
        return await _retry_transcription(
            lambda attempt: _transcribe_gemini_once(path, config, job_id=job_id, timestamps=False, attempt=attempt),
            job_id=job_id,
            protocol=PROTOCOL_GEMINI,
        )


async def _transcribe_gemini_once(
    path: Path, config: Any, *, job_id: str, timestamps: bool, attempt: int
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    origin = _gemini_origin(config.base_url)
    endpoint = f"{origin}/v1beta/interactions"
    audio_bytes = path.stat().st_size
    started = time.monotonic()
    request_fields = {
        "event": "provider.transcription.request",
        "job_id": job_id,
        "transcription_protocol": PROTOCOL_GEMINI,
        "provider_origin": origin,
        "model": config.model,
        "audio_bytes": audio_bytes,
        "audio_format": path.suffix.removeprefix(".").lower() or "wav",
        "timeout_sec": config.timeout_sec,
        "attempt": attempt,
        "response_format": "word_timestamps" if timestamps else "text",
    }
    logger.info("sending transcription request", extra=request_fields)
    file_name = ""
    try:
        async with httpx.AsyncClient(timeout=_timeout(config), follow_redirects=False) as client:
            try:
                file_uri, file_name = await _gemini_upload_file(client, origin, config, path)
                response = await client.post(
                    endpoint,
                    headers=_gemini_auth_headers(config.api_key, json_content=True),
                    json=_gemini_interaction_body(config, audio_uri=file_uri, timestamps=timestamps),
                )
            except httpx.TimeoutException as exc:
                _log_transport_failure("transcription", "timeout", request_fields, started, exc)
                raise ProviderError(
                    "provider_timeout", "transcription provider timed out", provider_error=str(exc)
                ) from exc
            except httpx.HTTPError as exc:
                _log_transport_failure("transcription", "unreachable", request_fields, started, exc)
                raise ProviderError(
                    "provider_unreachable", "transcription provider is unreachable", provider_error=str(exc)
                ) from exc
            _log_response("transcription", response, request_fields, started, config.api_key)
            _raise_for_status(response, "transcription")
            try:
                payload = response.json()
            except json.JSONDecodeError as exc:
                raise ProviderError(
                    "provider_invalid_response", "transcription provider returned invalid JSON"
                ) from exc
            return _gemini_transcription_result(path, payload, timestamps=timestamps)
    finally:
        if file_name:
            await _gemini_delete_file(origin, config, file_name)


async def _test_gemini(config: Any) -> int:
    origin = _gemini_origin(config.base_url)
    body = _gemini_interaction_body(
        config, audio_data=base64.b64encode(_probe_wav()).decode("ascii"), timestamps=False
    )
    try:
        async with httpx.AsyncClient(timeout=_timeout(config, probe=True), follow_redirects=False) as client:
            response = await client.post(
                f"{origin}/v1beta/interactions",
                headers=_gemini_auth_headers(config.api_key, json_content=True),
                json=body,
            )
    except httpx.TimeoutException as exc:
        raise ProviderError("provider_timeout", "transcription provider timed out", provider_error=str(exc)) from exc
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


async def _transcribe_chat_audio(
    path: Path, config: Any, *, job_id: str
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    return await _retry_transcription(
        lambda attempt: _transcribe_chat_audio_once(path, config, job_id=job_id, attempt=attempt),
        job_id=job_id,
        protocol=PROTOCOL_CHAT_AUDIO,
    )


async def _transcribe_chat_audio_once(
    path: Path, config: Any, *, job_id: str, attempt: int
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    wav_bytes = path.read_bytes()
    endpoint = f"{config.base_url.rstrip('/')}/chat/completions"
    started = time.monotonic()
    request_fields = {
        "event": "provider.transcription.request",
        "job_id": job_id,
        "transcription_protocol": PROTOCOL_CHAT_AUDIO,
        "provider_origin": _origin(endpoint),
        "model": config.model,
        "audio_bytes": len(wav_bytes),
        "audio_format": path.suffix.removeprefix(".").lower() or "wav",
        "timeout_sec": config.timeout_sec,
        "attempt": attempt,
        "response_format": "chat_audio",
    }
    logger.info("sending transcription request", extra=request_fields)
    try:
        async with httpx.AsyncClient(timeout=_timeout(config), follow_redirects=False) as client:
            response = await client.post(
                endpoint,
                headers={"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"},
                json=_chat_audio_body(config, wav_bytes, probe=False),
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
    _raise_for_status(response, "transcription")
    try:
        payload = response.json()
        text = str(payload["choices"][0]["message"]["content"]).strip()
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response") from exc
    if not text:
        raise ProviderError("provider_invalid_response", "transcription provider returned an empty transcript")
    duration = max(audio_duration_seconds(path), 0.001)
    usage = payload.get("usage") if isinstance(payload.get("usage"), dict) else {}
    return [{"start": 0.0, "end": duration, "text": text}], usage


async def _test_chat_audio(config: Any) -> int:
    body = _chat_audio_body(config, _probe_wav(), probe=True)
    try:
        async with httpx.AsyncClient(timeout=_timeout(config, probe=True), follow_redirects=False) as client:
            response = await client.post(
                f"{config.base_url.rstrip('/')}/chat/completions",
                headers={"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"},
                json=body,
            )
    except httpx.TimeoutException as exc:
        raise ProviderError("provider_timeout", "transcription provider timed out", provider_error=str(exc)) from exc
    except httpx.HTTPError as exc:
        raise ProviderError(
            "provider_unreachable", "transcription provider is unreachable", provider_error=str(exc)
        ) from exc
    _raise_for_status(response, "transcription")
    try:
        payload = response.json()
        message = payload["choices"][0]["message"]
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response") from exc
    if not isinstance(message, dict):
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response")
    return response.status_code


async def _retry_transcription(operation: Any, *, job_id: str, protocol: str) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    last_error: ProviderError | None = None
    for attempt in range(1, TRANSCRIBE_ATTEMPTS + 1):
        try:
            return await operation(attempt)
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
                    "transcription_protocol": protocol,
                    "error_code": exc.code,
                    "http_status": exc.status_code,
                },
            )
            await _sleep(_transcribe_retry_delay(attempt))
    assert last_error is not None
    raise last_error


def _gemini_origin(base_url: str) -> str:
    parsed = urlsplit(base_url.strip())
    scheme = parsed.scheme or "https"
    host = parsed.hostname or _GEMINI_HOST
    return f"{scheme}://{host}"


def _gemini_auth_headers(api_key: str, *, json_content: bool = False) -> dict[str, str]:
    headers = {"Authorization": f"Bearer {api_key}", "x-goog-api-key": api_key}
    if json_content:
        headers["Content-Type"] = "application/json"
    return headers


def _gemini_language_codes(language: str) -> list[str] | None:
    raw = language.strip()
    if not raw:
        return None
    mapped = _GEMINI_LANGUAGES.get(raw.lower().replace("_", "-"))
    return [mapped] if mapped else None


def _gemini_interaction_body(
    config: Any, *, audio_data: str | None = None, audio_uri: str | None = None, timestamps: bool
) -> dict[str, Any]:
    audio_part: dict[str, Any] = {"type": "audio", "mime_type": "audio/wav"}
    if audio_uri:
        audio_part["uri"] = audio_uri
    else:
        audio_part["data"] = audio_data or ""
    body: dict[str, Any] = {"model": config.model, "input": [audio_part]}
    transcription_config: dict[str, Any] = {}
    codes = _gemini_language_codes(getattr(config, "language", "") or "")
    if codes:
        transcription_config["language_codes"] = codes
    if timestamps:
        transcription_config["mode"] = {"type": "verbatim", "timestamp_granularities": ["word"]}
    if transcription_config:
        body["generation_config"] = {"transcription_config": transcription_config}
    return body


def _gemini_absolute_url(origin: str, url: str) -> str:
    parsed = urlsplit(url)
    if not parsed.scheme:
        return f"{origin.rstrip('/')}/{url.lstrip('/')}"
    if (parsed.hostname or "").lower() != _GEMINI_HOST or parsed.scheme != "https":
        raise ProviderError("provider_invalid_response", "gemini files API returned an unexpected upload host")
    return url


async def _gemini_upload_file(
    client: httpx.AsyncClient, origin: str, config: Any, path: Path
) -> tuple[str, str]:
    num_bytes = path.stat().st_size
    start = await client.post(
        f"{origin}/upload/v1beta/files",
        headers={
            **_gemini_auth_headers(config.api_key, json_content=True),
            "X-Goog-Upload-Protocol": "resumable",
            "X-Goog-Upload-Command": "start",
            "X-Goog-Upload-Header-Content-Length": str(num_bytes),
            "X-Goog-Upload-Header-Content-Type": "audio/wav",
        },
        json={"file": {"display_name": path.name}},
    )
    _raise_for_status(start, "transcription")
    upload_url = start.headers.get("x-goog-upload-url")
    if not upload_url:
        raise ProviderError("provider_invalid_response", "gemini files API did not return an upload URL")
    upload_url = _gemini_absolute_url(origin, upload_url)
    uploaded = await client.post(
        upload_url,
        headers={
            **_gemini_auth_headers(config.api_key),
            "Content-Length": str(num_bytes),
            "X-Goog-Upload-Offset": "0",
            "X-Goog-Upload-Command": "upload, finalize",
        },
        content=path.read_bytes(),
    )
    _raise_for_status(uploaded, "transcription")
    try:
        payload = uploaded.json()
    except json.JSONDecodeError as exc:
        raise ProviderError("provider_invalid_response", "gemini files API returned invalid JSON") from exc
    file_obj = payload.get("file") if isinstance(payload, dict) else None
    if not isinstance(file_obj, dict):
        file_obj = payload if isinstance(payload, dict) else None
    if not isinstance(file_obj, dict):
        raise ProviderError("provider_invalid_response", "gemini files API returned an invalid response")
    name = str(file_obj.get("name") or "").strip()
    uri = str(file_obj.get("uri") or "").strip()
    if not name:
        raise ProviderError("provider_invalid_response", "gemini files API returned an invalid response")
    if not uri:
        uri = f"{origin}/v1beta/{name.lstrip('/')}"
    state = str(file_obj.get("state") or "ACTIVE")
    if state not in {"", "ACTIVE", "STATE_UNSPECIFIED"}:
        uri, name = await _gemini_wait_active(client, origin, config, name)
    return uri, name


async def _gemini_wait_active(
    client: httpx.AsyncClient, origin: str, config: Any, file_name: str
) -> tuple[str, str]:
    for _ in range(10):
        await _sleep(0.5)
        response = await client.get(
            f"{origin}/v1beta/{file_name.lstrip('/')}",
            headers=_gemini_auth_headers(config.api_key),
        )
        _raise_for_status(response, "transcription")
        try:
            payload = response.json()
        except json.JSONDecodeError as exc:
            raise ProviderError("provider_invalid_response", "gemini files API returned invalid JSON") from exc
        file_obj = payload.get("file") if isinstance(payload, dict) and isinstance(payload.get("file"), dict) else payload
        if not isinstance(file_obj, dict):
            raise ProviderError("provider_invalid_response", "gemini files API returned an invalid response")
        state = str(file_obj.get("state") or "")
        if state in {"FAILED", "STATE_FAILED"}:
            raise ProviderError("provider_failure", "gemini files API failed to process uploaded audio")
        if state in {"", "ACTIVE", "STATE_UNSPECIFIED"}:
            uri = str(file_obj.get("uri") or "").strip() or f"{origin}/v1beta/{file_name.lstrip('/')}"
            return uri, str(file_obj.get("name") or file_name)
    raise ProviderError("provider_timeout", "gemini files API did not become ready")


async def _gemini_delete_file(origin: str, config: Any, file_name: str) -> None:
    try:
        async with httpx.AsyncClient(timeout=_timeout(config), follow_redirects=False) as client:
            await client.delete(
                f"{origin}/v1beta/{file_name.lstrip('/')}",
                headers=_gemini_auth_headers(config.api_key),
            )
    except httpx.HTTPError:
        logger.warning(
            "failed to delete gemini uploaded file",
            extra={"event": "provider.transcription.gemini_delete_failed"},
        )


def _parse_offset(value: Any) -> float | None:
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return float(value)
    if not isinstance(value, str):
        return None
    text = value.strip().rstrip("sS")
    try:
        parsed = float(text)
    except ValueError:
        return None
    return parsed if parsed >= 0 else None


def _is_cjk(char: str) -> bool:
    code = ord(char)
    return (
        0x3400 <= code <= 0x9FFF
        or 0xF900 <= code <= 0xFAFF
        or 0x3040 <= code <= 0x30FF
        or 0xAC00 <= code <= 0xD7AF
    )


def _join_transcript(left: str, right: str) -> str:
    if not left:
        return right
    if not right:
        return left
    if left[-1].isspace() or right[0].isspace():
        return left + right
    if _is_cjk(left[-1]) or _is_cjk(right[0]) or left[-1] in "([{（【「『":
        return left + right
    return f"{left} {right}"


def _gemini_word_annotations(payload: dict[str, Any]) -> list[dict[str, Any]]:
    words: list[dict[str, Any]] = []
    for step in payload.get("steps") or []:
        if not isinstance(step, dict):
            continue
        for content in step.get("content") or []:
            if not isinstance(content, dict):
                continue
            for annotation in content.get("annotations") or []:
                if not isinstance(annotation, dict) or annotation.get("type") != "word_info":
                    continue
                text = str(annotation.get("text") or "").strip()
                start = _parse_offset(annotation.get("start_offset"))
                end = _parse_offset(annotation.get("end_offset"))
                if text and start is not None and end is not None and end >= start:
                    words.append({"start": start, "end": end, "text": text})
    return words


def _group_words(words: list[dict[str, Any]], gap: float = GEMINI_WORD_GAP_SEC) -> list[dict[str, Any]]:
    if not words:
        return []
    segments = [
        {"start": words[0]["start"], "end": words[0]["end"], "text": words[0]["text"]},
    ]
    for word in words[1:]:
        current = segments[-1]
        ends_sentence = current["text"].endswith(("。", "！", "？", ".", "!", "?", "；", ";"))
        if word["start"] - current["end"] > gap or ends_sentence:
            segments.append({"start": word["start"], "end": word["end"], "text": word["text"]})
            continue
        current["text"] = _join_transcript(current["text"], word["text"])
        current["end"] = word["end"]
    return segments


def _gemini_output_text(payload: dict[str, Any]) -> str:
    text = payload.get("output_text")
    if isinstance(text, str) and text.strip():
        return text.strip()
    parts: list[str] = []
    for step in payload.get("steps") or []:
        if not isinstance(step, dict):
            continue
        for content in step.get("content") or []:
            if isinstance(content, dict) and isinstance(content.get("text"), str) and content["text"].strip():
                parts.append(content["text"].strip())
    return "\n".join(parts).strip()


def _gemini_transcription_result(path: Path, payload: Any, *, timestamps: bool) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    if not isinstance(payload, dict):
        raise ProviderError("provider_invalid_response", "transcription provider returned an invalid response")
    usage = _usage(payload)
    if timestamps:
        segments = _group_words(_gemini_word_annotations(payload))
        if segments:
            return segments, usage
    text = _gemini_output_text(payload)
    if text:
        duration = max(_payload_duration_seconds(payload, path), 0.001)
        return [{"start": 0.0, "end": duration, "text": text}], usage
    if timestamps:
        raise ProviderError("timestamps_unsupported", "transcription provider did not return segment timestamps")
    raise ProviderError("provider_invalid_response", "transcription provider returned an empty transcript")


def _chat_audio_body(config: Any, wav_bytes: bytes, *, probe: bool) -> dict[str, Any]:
    prompt = str(getattr(config, "prompt", "") or "").strip()
    instruction = PROBE_CHAT_AUDIO_PROMPT if probe else (prompt or DEFAULT_CHAT_AUDIO_PROMPT)
    return {
        "model": config.model,
        "messages": [
            {
                "role": "user",
                "content": [
                    {
                        "type": "input_audio",
                        "input_audio": {
                            "data": f"data:audio/wav;base64,{base64.b64encode(wav_bytes).decode('ascii')}",
                            "format": "wav",
                        },
                    },
                    {"type": "text", "text": instruction},
                ],
            }
        ],
        "temperature": 0,
        "enable_thinking": False,
        "stream": False,
    }


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

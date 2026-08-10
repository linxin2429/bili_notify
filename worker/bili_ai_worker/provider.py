from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import httpx


class ProviderError(RuntimeError):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


async def transcribe(path: Path, config: Any) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    fields: list[tuple[str, str]] = [
        ("model", config.model),
        ("response_format", "verbose_json"),
        ("timestamp_granularities[]", "segment"),
    ]
    if config.language:
        fields.append(("language", config.language))
    if config.prompt:
        fields.append(("prompt", config.prompt))
    headers = {"Authorization": f"Bearer {config.api_key}"}
    timeout = httpx.Timeout(float(config.timeout_sec), connect=15.0)
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=False) as client:
            with path.open("rb") as audio:
                response = await client.post(
                    f"{config.base_url.rstrip('/')}/audio/transcriptions",
                    headers=headers,
                    data=fields,
                    files={"file": (path.name, audio, "audio/flac")},
                )
    except httpx.TimeoutException as exc:
        raise ProviderError("provider_timeout", "transcription provider timed out") from exc
    except httpx.HTTPError as exc:
        raise ProviderError("provider_unreachable", "transcription provider is unreachable") from exc
    if response.status_code == 401 or response.status_code == 403:
        raise ProviderError("provider_authentication", "transcription provider rejected the API key")
    if response.status_code == 429:
        raise ProviderError("provider_rate_limited", "transcription provider rate limited the request")
    if response.status_code < 200 or response.status_code >= 300:
        raise ProviderError("provider_failure", f"transcription provider returned HTTP {response.status_code}")
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


async def complete(messages: list[dict[str, str]], config: Any) -> tuple[str, dict[str, Any]]:
    body = {
        "model": config.model,
        "messages": messages,
        "temperature": config.temperature,
        "max_tokens": config.max_output_tokens,
    }
    headers = {"Authorization": f"Bearer {config.api_key}", "Content-Type": "application/json"}
    timeout = httpx.Timeout(float(config.timeout_sec), connect=15.0)
    try:
        async with httpx.AsyncClient(timeout=timeout, follow_redirects=False) as client:
            response = await client.post(
                f"{config.base_url.rstrip('/')}/chat/completions", headers=headers, json=body
            )
    except httpx.TimeoutException as exc:
        raise ProviderError("provider_timeout", "text provider timed out") from exc
    except httpx.HTTPError as exc:
        raise ProviderError("provider_unreachable", "text provider is unreachable") from exc
    if response.status_code == 401 or response.status_code == 403:
        raise ProviderError("provider_authentication", "text provider rejected the API key")
    if response.status_code == 429:
        raise ProviderError("provider_rate_limited", "text provider rate limited the request")
    if response.status_code < 200 or response.status_code >= 300:
        raise ProviderError("provider_failure", f"text provider returned HTTP {response.status_code}")
    try:
        payload = response.json()
        text = str(payload["choices"][0]["message"]["content"]).strip()
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
        raise ProviderError("provider_invalid_response", "text provider returned an invalid response") from exc
    if not text:
        raise ProviderError("provider_invalid_response", "text provider returned an empty summary")
    usage = payload.get("usage") if isinstance(payload.get("usage"), dict) else {}
    return text, usage

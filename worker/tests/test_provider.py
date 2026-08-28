import wave
from io import BytesIO
from pathlib import Path
from types import SimpleNamespace
from typing import ClassVar, Self

import httpx
import pytest

from bili_ai_worker import provider


class SequenceClient:
    def __init__(self, responses: list[httpx.Response | BaseException]) -> None:
        self.responses = list(responses)
        self.requests: list[tuple[tuple[object, ...], dict[str, object]]] = []

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_args: object) -> None:
        return None

    async def post(self, *args: object, **kwargs: object) -> httpx.Response:
        self.requests.append((args, kwargs))
        if not self.responses:
            raise AssertionError("unexpected extra transcription request")
        item = self.responses.pop(0)
        if isinstance(item, BaseException):
            raise item
        return item


def install_sequence(
    monkeypatch: pytest.MonkeyPatch, responses: list[httpx.Response | BaseException]
) -> SequenceClient:
    client = SequenceClient(responses)

    def factory(**_kwargs: object) -> SequenceClient:
        return client

    monkeypatch.setattr(provider.httpx, "AsyncClient", factory)
    return client


async def instant_sleep(_seconds: float) -> None:
    return None


class FakeClient:
    response: httpx.Response
    requests: ClassVar[list[tuple[tuple[object, ...], dict[str, object]]]] = []

    def __init__(self, **_kwargs: object) -> None:
        pass

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_args: object) -> None:
        return None

    async def post(self, *args: object, **kwargs: object) -> httpx.Response:
        self.requests.append((args, kwargs))
        return self.response


def config() -> SimpleNamespace:
    return SimpleNamespace(
        base_url="https://provider.example/v1",
        api_key="secret",
        model="model",
        language="zh",
        prompt="",
        temperature=0.2,
        max_output_tokens=100,
        timeout_sec=60,
    )


@pytest.mark.asyncio
async def test_transcribe_normalizes_segments(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    FakeClient.response = httpx.Response(200, json={"segments": [{"start": 1.25, "end": 2, "text": " hello "}], "usage": {"seconds": 1}})
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    segments, usage = await provider.transcribe(audio, config())

    assert segments == [{"start": 1.25, "end": 2.0, "text": "hello"}]
    assert usage == {"seconds": 1}


@pytest.mark.parametrize(
    ("status", "code"),
    [
        (401, "provider_authentication"),
        (404, "provider_model_not_found"),
        (413, "provider_payload_too_large"),
        (429, "provider_rate_limited"),
        (500, "provider_failure"),
    ],
)
@pytest.mark.asyncio
async def test_complete_classifies_provider_errors(status: int, code: str, monkeypatch: pytest.MonkeyPatch) -> None:
    FakeClient.response = httpx.Response(status, json={"error": {"message": "provider detail"}})
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    with pytest.raises(provider.ProviderError) as caught:
        await provider.complete([{"role": "user", "content": "text"}], config())

    assert caught.value.code == code
    assert caught.value.status_code == status
    assert caught.value.provider_error == "provider detail"


@pytest.mark.asyncio
async def test_transcribe_reports_payload_size_for_http_413(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    FakeClient.response = httpx.Response(
        413,
        headers={"x-request-id": "request-123"},
        json={"error": {"message": "request body exceeds the provider limit"}},
    )
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    with pytest.raises(provider.ProviderError) as caught:
        await provider.transcribe(audio, config(), job_id="job-123")

    assert caught.value.code == "provider_payload_too_large"
    assert caught.value.status_code == 413
    assert "5-byte audio chunk" in str(caught.value)


@pytest.mark.asyncio
async def test_provider_log_redacts_api_key(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    FakeClient.response = httpx.Response(
        413,
        headers={"x-generation-id": "generation-123"},
        json={"error": {"message": "secret exceeds the provider limit"}},
    )
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    with pytest.raises(provider.ProviderError):
        await provider.transcribe(audio, config(), job_id="job-123")

    response_log = next(record for record in caplog.records if record.event == "provider.transcription.response")
    assert response_log.provider_error == "[REDACTED] exceeds the provider limit"
    assert response_log.provider_request_id == "generation-123"
    assert response_log.audio_bytes == 5
    assert response_log.job_id == "job-123"


@pytest.mark.parametrize(("value", "included"), [(0, False), (1 << 40, True)])
@pytest.mark.asyncio
async def test_complete_only_sends_configured_max_tokens(
    value: int, included: bool, monkeypatch: pytest.MonkeyPatch
) -> None:
    current = config()
    current.max_output_tokens = value
    FakeClient.requests = []
    FakeClient.response = httpx.Response(200, json={"choices": [{"message": {"content": "ok"}}]})
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    await provider.complete([{"role": "user", "content": "text"}], current)

    body = FakeClient.requests[0][1]["json"]
    assert isinstance(body, dict)
    assert ("max_tokens" in body) is included
    if included:
        assert body["max_tokens"] == value


@pytest.mark.parametrize(
    ("kind", "response", "path"),
    [
        ("text", {"choices": [{"message": {"content": "OK"}}]}, "/chat/completions"),
        ("transcription", {"text": ""}, "/audio/transcriptions"),
    ],
)
@pytest.mark.asyncio
async def test_provider_probe_calls_the_real_inference_endpoint(
    kind: str, response: dict[str, object], path: str, monkeypatch: pytest.MonkeyPatch
) -> None:
    FakeClient.requests = []
    FakeClient.response = httpx.Response(200, json=response)
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    status = await provider.test_provider(kind, config())

    assert status == 200
    args, kwargs = FakeClient.requests[0]
    assert str(args[0]).endswith(path)
    if kind == "transcription":
        files = kwargs["files"]
        assert isinstance(files, dict)
        wav = files["file"][1]
        with wave.open(BytesIO(wav), "rb") as audio:
            assert audio.getnchannels() == 1
            assert audio.getframerate() == 8_000
            assert audio.getnframes() > 0


@pytest.mark.parametrize("operation", ["transcribe", "probe"])
@pytest.mark.asyncio
async def test_transcription_multipart_body_is_async_compatible(
    operation: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    real_async_client = httpx.AsyncClient
    bodies: list[bytes] = []

    async def handle(request: httpx.Request) -> httpx.Response:
        bodies.append(await request.aread())
        payload = {"segments": [{"start": 0, "end": 1, "text": "hello"}]} if operation == "transcribe" else {}
        return httpx.Response(200, json=payload)

    def client_factory(**kwargs: object) -> httpx.AsyncClient:
        return real_async_client(transport=httpx.MockTransport(handle), **kwargs)

    monkeypatch.setattr(provider.httpx, "AsyncClient", client_factory)
    if operation == "transcribe":
        audio = tmp_path / "audio.wav"
        audio.write_bytes(b"audio")
        await provider.transcribe(audio, config())
    else:
        await provider.test_provider("transcription", config())

    assert len(bodies) == 1
    assert b"audio/wav" in bodies[0]
    assert b'name="timestamp_granularities[]"' in bodies[0]


@pytest.mark.parametrize(
    ("payload", "expected"),
    [
        (
            {
                "error": {
                    "message": "Provider returned 400",
                    "metadata": {"provider_name": "DeepInfra", "raw": "unsupported file format: flac"},
                }
            },
            "Provider returned 400 DeepInfra unsupported file format: flac",
        ),
        ({"error": {"message": "invalid request"}}, "invalid request"),
        ({"detail": "nope"}, "nope"),
    ],
)
def test_error_message_includes_openrouter_metadata(payload: dict[str, object], expected: str) -> None:
    response = httpx.Response(400, json=payload)
    assert provider._error_message(response) == expected


@pytest.mark.parametrize(
    ("status", "code", "requests"),
    [
        (401, "provider_authentication", 1),
        (404, "provider_model_not_found", 1),
        (413, "provider_payload_too_large", 1),
    ],
)
@pytest.mark.asyncio
async def test_transcribe_does_not_retry_permanent_errors(
    status: int, code: str, requests: int, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    client = install_sequence(
        monkeypatch,
        [httpx.Response(status, json={"error": {"message": "permanent"}})],
    )
    monkeypatch.setattr(provider, "_sleep", instant_sleep)

    with pytest.raises(provider.ProviderError) as caught:
        await provider.transcribe(audio, config(), job_id="job-1")

    assert caught.value.code == code
    assert len(client.requests) == requests


@pytest.mark.asyncio
async def test_transcribe_retries_http_400_then_succeeds(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    client = install_sequence(
        monkeypatch,
        [
            httpx.Response(400, json={"error": {"message": "Provider returned 400"}}),
            httpx.Response(200, json={"segments": [{"start": 0, "end": 1, "text": "hello"}]}),
        ],
    )
    monkeypatch.setattr(provider, "_sleep", instant_sleep)

    segments, _usage = await provider.transcribe(audio, config(), job_id="job-1")

    assert segments == [{"start": 0.0, "end": 1.0, "text": "hello"}]
    assert len(client.requests) == 2


@pytest.mark.asyncio
async def test_transcribe_retries_unreachable_then_succeeds(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    client = install_sequence(
        monkeypatch,
        [
            httpx.ConnectError("connection reset"),
            httpx.Response(200, json={"segments": [{"start": 0, "end": 1, "text": "hello"}]}),
        ],
    )
    monkeypatch.setattr(provider, "_sleep", instant_sleep)

    segments, _usage = await provider.transcribe(audio, config(), job_id="job-1")

    assert segments[0]["text"] == "hello"
    assert len(client.requests) == 2


@pytest.mark.asyncio
async def test_transcribe_falls_back_to_json_without_timestamps(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    failures = [
        httpx.Response(
            400,
            json={"error": {"message": "Provider returned 400", "metadata": {"provider_name": "DeepInfra"}}},
        )
        for _ in range(provider.TRANSCRIBE_ATTEMPTS)
    ]
    client = install_sequence(
        monkeypatch,
        [*failures, httpx.Response(200, json={"text": "spoken words", "duration": 12.5})],
    )
    monkeypatch.setattr(provider, "_sleep", instant_sleep)

    segments, _usage = await provider.transcribe(audio, config(), job_id="job-1")

    assert segments == [{"start": 0.0, "end": 12.5, "text": "spoken words"}]
    assert len(client.requests) == provider.TRANSCRIBE_ATTEMPTS + 1
    fallback_fields = client.requests[-1][1]["data"]
    assert isinstance(fallback_fields, dict)
    assert fallback_fields["response_format"] == "json"
    assert "timestamp_granularities[]" not in fallback_fields


@pytest.mark.asyncio
async def test_transcribe_falls_back_when_verbose_json_omits_segments(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio")
    missing = [httpx.Response(200, json={"text": "ignored"}) for _ in range(provider.TRANSCRIBE_ATTEMPTS)]
    client = install_sequence(
        monkeypatch,
        [*missing, httpx.Response(200, json={"text": "fallback", "usage": {"seconds": 3}})],
    )
    monkeypatch.setattr(provider, "_sleep", instant_sleep)

    segments, usage = await provider.transcribe(audio, config(), job_id="job-1")

    assert segments == [{"start": 0.0, "end": 3.0, "text": "fallback"}]
    assert usage == {"seconds": 3}
    assert len(client.requests) == provider.TRANSCRIBE_ATTEMPTS + 1

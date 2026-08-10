import wave
from io import BytesIO
from pathlib import Path
from types import SimpleNamespace
from typing import ClassVar, Self

import httpx
import pytest

from bili_ai_worker import provider


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
    audio = tmp_path / "audio.flac"
    audio.write_bytes(b"audio")
    FakeClient.response = httpx.Response(200, json={"segments": [{"start": 1.25, "end": 2, "text": " hello "}], "usage": {"seconds": 1}})
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    segments, usage = await provider.transcribe(audio, config())

    assert segments == [{"start": 1.25, "end": 2.0, "text": "hello"}]
    assert usage == {"seconds": 1}


@pytest.mark.parametrize(
    ("status", "code"),
    [(401, "provider_authentication"), (404, "provider_model_not_found"), (429, "provider_rate_limited"), (500, "provider_failure")],
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
        audio = tmp_path / "audio.flac"
        audio.write_bytes(b"audio")
        await provider.transcribe(audio, config())
    else:
        await provider.test_provider("transcription", config())

    assert len(bodies) == 1
    assert b'name="timestamp_granularities[]"' in bodies[0]

from pathlib import Path
from types import SimpleNamespace
from typing import Self

import httpx
import pytest

from bili_ai_worker import provider


class FakeClient:
    response: httpx.Response

    def __init__(self, **_kwargs: object) -> None:
        pass

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *_args: object) -> None:
        return None

    async def post(self, *_args: object, **_kwargs: object) -> httpx.Response:
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


@pytest.mark.parametrize(("status", "code"), [(401, "provider_authentication"), (429, "provider_rate_limited"), (500, "provider_failure")])
@pytest.mark.asyncio
async def test_complete_classifies_provider_errors(status: int, code: str, monkeypatch: pytest.MonkeyPatch) -> None:
    FakeClient.response = httpx.Response(status)
    monkeypatch.setattr(provider.httpx, "AsyncClient", FakeClient)

    with pytest.raises(provider.ProviderError) as caught:
        await provider.complete([{"role": "user", "content": "text"}], config())

    assert caught.value.code == code

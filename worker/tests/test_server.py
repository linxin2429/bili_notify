import wave
from pathlib import Path
from types import SimpleNamespace

import pytest

from bili_ai_worker import server
from bili_ai_worker.media import (
    CHUNK_DURATION_MS,
    DOWNLOAD_RETRIES,
    DOWNLOAD_SOCKET_TIMEOUT_SEC,
    MIN_CHUNK_DURATION_SEC,
    _download_options,
    _merge_short_trailing_chunk,
    _retry_delay,
    _split_audio_command,
    audio_duration_seconds,
    cleanup_cache,
    write_cookie_file,
)
from bili_ai_worker.provider import ProviderError
from bili_ai_worker.server import _chunks, _messages, _struct


@pytest.mark.parametrize(
    ("text", "limit", "expected"),
    [
        ("one\ntwo", 10, ["one\ntwo"]),
        ("abcdefgh", 3, ["abc", "def", "gh"]),
        ("", 10, [""]),
    ],
)
def test_chunks(text: str, limit: int, expected: list[str]) -> None:
    assert _chunks(text, limit) == expected


def test_messages_and_struct() -> None:
    assert _messages(" system ", "user") == [
        {"role": "system", "content": " system "},
        {"role": "user", "content": "user"},
    ]
    assert dict(_struct({"input_tokens": 3})) == {"input_tokens": 3.0}


def test_cookie_file_and_cache_cleanup(tmp_path: Path) -> None:
    cookie = tmp_path / "cookies.txt"
    write_cookie_file(cookie, {"SESSDATA": "secret", "bad\tname": "ignored"})
    content = cookie.read_text(encoding="utf-8")
    assert "SESSDATA\tsecret" in content
    assert "bad\tname" not in content

    cache = tmp_path / "cache"
    entry = cache / "job"
    entry.mkdir(parents=True)
    (entry / "audio").write_bytes(b"1234")
    assert cleanup_cache(cache, 0, 3) == 0
    assert not entry.exists()


@pytest.mark.parametrize(
    ("attempt", "expected"),
    [(0, 1), (1, 1), (2, 2), (6, 30), (100, 30)],
)
def test_download_retry_delay(attempt: int, expected: int) -> None:
    assert _retry_delay(attempt) == expected


def test_download_network_budget_is_resilient(tmp_path: Path) -> None:
    options = _download_options(tmp_path / "cookies.txt")

    assert options["socket_timeout"] == DOWNLOAD_SOCKET_TIMEOUT_SEC == 60
    assert options["retries"] == DOWNLOAD_RETRIES == 10
    assert options["fragment_retries"] == DOWNLOAD_RETRIES
    assert options["extractor_retries"] == DOWNLOAD_RETRIES
    retry_sleep = options["retry_sleep_functions"]
    assert isinstance(retry_sleep, dict)
    assert set(retry_sleep) == {"http", "fragment", "extractor"}


def test_split_audio_forces_16_bit_pcm_wav() -> None:
    command = _split_audio_command(Path("input.flac"), Path("chunk-%04d.wav"))

    assert command[command.index("-sample_fmt") + 1] == "s16"
    assert command[command.index("-c:a") + 1] == "pcm_s16le"
    assert command[command.index("-ar") + 1] == "16000"
    assert command[command.index("-ac") + 1] == "1"
    assert command[command.index("-segment_time") + 1] == str(CHUNK_DURATION_MS // 1000)
    assert command[-1].endswith(".wav")


def _write_silence(path: Path, duration_sec: float, sample_rate: int = 16_000) -> None:
    frames = max(round(sample_rate * duration_sec), 0)
    with wave.open(str(path), "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(sample_rate)
        audio.writeframes(b"\x00\x00" * frames)


def test_merge_short_trailing_wav_chunk(tmp_path: Path) -> None:
    first = tmp_path / "chunk-0000.wav"
    last = tmp_path / "chunk-0001.wav"
    _write_silence(first, 2.0)
    _write_silence(last, 0.25)

    merged = _merge_short_trailing_chunk([first, last])

    assert merged == [first]
    assert first.exists()
    assert not last.exists()
    assert audio_duration_seconds(first) == pytest.approx(2.25, abs=0.01)


def test_keeps_trailing_chunk_when_long_enough(tmp_path: Path) -> None:
    first = tmp_path / "chunk-0000.wav"
    last = tmp_path / "chunk-0001.wav"
    _write_silence(first, 2.0)
    _write_silence(last, MIN_CHUNK_DURATION_SEC)

    kept = _merge_short_trailing_chunk([first, last])

    assert kept == [first, last]
    assert first.exists()
    assert last.exists()


def test_single_short_chunk_is_not_merged(tmp_path: Path) -> None:
    only = tmp_path / "chunk-0000.wav"
    _write_silence(only, 0.2)

    assert _merge_short_trailing_chunk([only]) == [only]


@pytest.mark.asyncio
async def test_provider_probe_returns_structured_provider_error(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def failed_probe(_kind: str, _provider: object) -> int:
        raise ProviderError(
            "provider_authentication",
            "provider rejected the API key",
            status_code=401,
            provider_error="Invalid API key",
        )

    monkeypatch.setattr(server, "test_provider", failed_probe)
    worker = server.AIWorker(tmp_path)
    result = await worker.TestProvider(
        SimpleNamespace(kind="text", provider=SimpleNamespace(model="test-model")), None
    )

    assert not result.ok
    assert result.error_code == "provider_authentication"
    assert result.provider_http_status == 401
    assert result.provider_error == "Invalid API key"

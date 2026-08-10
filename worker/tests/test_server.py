from pathlib import Path
from types import SimpleNamespace

import pytest

from bili_ai_worker import server
from bili_ai_worker.media import (
    DOWNLOAD_RETRIES,
    DOWNLOAD_SOCKET_TIMEOUT_SEC,
    _download_options,
    _retry_delay,
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
    result = await worker.TestProvider(SimpleNamespace(kind="text", provider=object()), None)

    assert not result.ok
    assert result.error_code == "provider_authentication"
    assert result.provider_http_status == 401
    assert result.provider_error == "Invalid API key"

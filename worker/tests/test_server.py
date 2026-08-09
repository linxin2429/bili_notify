from pathlib import Path

import pytest

from bili_ai_worker.media import cleanup_cache, write_cookie_file
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

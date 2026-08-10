from __future__ import annotations

import asyncio
import os
import shutil
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlencode

import yt_dlp

DOWNLOAD_SOCKET_TIMEOUT_SEC = 60
DOWNLOAD_RETRIES = 10


class DownloadError(RuntimeError):
    pass


@dataclass(frozen=True)
class DownloadedPage:
    page: int
    cid: str
    title: str
    duration_ms: int
    audio_path: Path


def write_cookie_file(path: Path, cookies: dict[str, str]) -> None:
    lines = ["# Netscape HTTP Cookie File"]
    for name, value in sorted(cookies.items()):
        if name and value and not any(character in name or character in value for character in "\t\r\n"):
            lines.append(f".bilibili.com\tTRUE\t/\tTRUE\t0\t{name}\t{value}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    os.chmod(path, 0o600)


async def download_pages(cache_dir: Path, job_id: str, bvid: str, page: int, cookies: dict[str, str]) -> tuple[str, list[DownloadedPage]]:
    job_dir = cache_dir / job_id
    job_dir.mkdir(parents=True, mode=0o700, exist_ok=True)
    cookie_path = job_dir / "cookies.txt"
    write_cookie_file(cookie_path, cookies)
    try:
        return await asyncio.to_thread(_download_pages, job_dir, cookie_path, bvid, page)
    except yt_dlp.utils.DownloadError as exc:
        raise DownloadError("Bilibili audio download failed") from exc
    finally:
        cookie_path.unlink(missing_ok=True)


def _download_pages(job_dir: Path, cookie_path: Path, bvid: str, selected_page: int) -> tuple[str, list[DownloadedPage]]:
    base_url = f"https://www.bilibili.com/video/{bvid}"
    common = _download_options(cookie_path)
    with yt_dlp.YoutubeDL(common) as ydl:
        info = ydl.extract_info(base_url, download=False)
    title = str(info.get("title") or bvid)
    page_count = int(info.get("page_count") or 1)
    page_numbers = [selected_page] if selected_page > 0 else list(range(1, page_count + 1))
    if any(number < 1 or number > page_count for number in page_numbers):
        raise DownloadError(f"requested page is outside 1..{page_count}")
    results: list[DownloadedPage] = []
    for number in page_numbers:
        output = str(job_dir / f"page-{number}.%(ext)s")
        options = {
            **common,
            "format": "bestaudio/best",
            "outtmpl": output,
            "postprocessors": [{"key": "FFmpegExtractAudio", "preferredcodec": "flac"}],
        }
        url = f"{base_url}?{urlencode({'p': number})}"
        with yt_dlp.YoutubeDL(options) as ydl:
            page_info = ydl.extract_info(url, download=True)
        candidates = sorted(job_dir.glob(f"page-{number}.*"))
        audio = next((candidate for candidate in candidates if candidate.suffix == ".flac"), None)
        if audio is None:
            raise DownloadError("yt-dlp did not produce an audio file")
        results.append(
            DownloadedPage(
                page=number,
                cid=str(page_info.get("cid") or ""),
                title=str(page_info.get("title") or f"P{number}"),
                duration_ms=int(float(page_info.get("duration") or 0) * 1000),
                audio_path=audio,
            )
        )
    return title, results


def _download_options(cookie_path: Path) -> dict[str, Any]:
    return {
        "quiet": True,
        "no_warnings": True,
        "cookiefile": str(cookie_path),
        "noplaylist": True,
        "socket_timeout": DOWNLOAD_SOCKET_TIMEOUT_SEC,
        "retries": DOWNLOAD_RETRIES,
        "fragment_retries": DOWNLOAD_RETRIES,
        "extractor_retries": DOWNLOAD_RETRIES,
        "retry_sleep_functions": {
            "http": _retry_delay,
            "fragment": _retry_delay,
            "extractor": _retry_delay,
        },
    }


def _retry_delay(attempt: int) -> int:
    return min(2 ** max(attempt - 1, 0), 30)


async def split_audio(page: DownloadedPage) -> list[tuple[Path, int]]:
    chunk_dir = page.audio_path.parent / f"chunks-{page.page}"
    chunk_dir.mkdir(mode=0o700, exist_ok=True)
    pattern = chunk_dir / "chunk-%04d.flac"
    process = await asyncio.create_subprocess_exec(
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", str(page.audio_path),
        "-vn", "-ac", "1", "-ar", "16000", "-f", "segment", "-segment_time", "600",
        "-reset_timestamps", "1", str(pattern),
        stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.PIPE,
    )
    try:
        _, stderr = await process.communicate()
    except asyncio.CancelledError:
        process.terminate()
        await process.wait()
        raise
    if process.returncode != 0:
        raise DownloadError(f"ffmpeg failed: {stderr.decode('utf-8', errors='replace')[:300]}")
    chunks = sorted(chunk_dir.glob("chunk-*.flac"))
    if not chunks:
        raise DownloadError("ffmpeg produced no audio chunks")
    return [(chunk, index * 600_000) for index, chunk in enumerate(chunks)]


def cleanup_cache(cache_dir: Path, ttl_sec: int, max_bytes: int) -> int:
    cache_dir.mkdir(parents=True, mode=0o700, exist_ok=True)
    now = time.time()
    entries: list[tuple[Path, float, int]] = []
    for path in cache_dir.iterdir():
        if not path.is_dir() or path.is_symlink():
            continue
        size = sum(item.stat().st_size for item in path.rglob("*") if item.is_file() and not item.is_symlink())
        entries.append((path, path.stat().st_mtime, size))
    for path, modified, _ in entries:
        if ttl_sec > 0 and now - modified > ttl_sec:
            shutil.rmtree(path, ignore_errors=True)
    remaining = [(path, modified, size) for path, modified, size in entries if path.exists()]
    total = sum(size for _, _, size in remaining)
    for path, _, size in sorted(remaining, key=lambda item: item[1]):
        if max_bytes <= 0 or total <= max_bytes:
            break
        shutil.rmtree(path, ignore_errors=True)
        total -= size
    return max(total, 0)

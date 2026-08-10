from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import shutil
import time
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any

import grpc
from google.protobuf.json_format import ParseDict
from google.protobuf.struct_pb2 import Struct
from opentelemetry.instrumentation.grpc import aio_server_interceptor, filters

from ai.v1 import worker_pb2, worker_pb2_grpc
from bili_ai_worker import __version__
from bili_ai_worker.log import configure_logging
from bili_ai_worker.media import DownloadError, cleanup_cache, download_pages, split_audio
from bili_ai_worker.provider import ProviderError, complete, test_provider, transcribe
from bili_ai_worker.telemetry import audio_bytes as audio_bytes_metric
from bili_ai_worker.telemetry import cache_bytes as cache_bytes_metric
from bili_ai_worker.telemetry import configure_telemetry, job_duration, jobs

logger = logging.getLogger("bili_ai_worker.server")


def _struct(value: dict[str, Any]):
    return ParseDict(value, Struct())


def _sum_usage(target: dict[str, Any], source: dict[str, Any]) -> None:
    for key, value in source.items():
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            target[key] = target.get(key, 0) + value


class AIWorker(worker_pb2_grpc.AIWorkerServicer):
    def __init__(self, cache_dir: Path) -> None:
        self.cache_dir = cache_dir
        self.active_transcriptions = 0
        self.active_summaries = 0

    async def GetCapabilities(self, request, context):
        del request, context
        cache_bytes = await asyncio.to_thread(cleanup_cache, self.cache_dir, 24 * 3600, 5 << 30)
        cache_bytes_metric.set(cache_bytes)
        logger.debug(
            "worker capabilities requested",
            extra={
                "event": "worker.capabilities",
                "active_transcriptions": self.active_transcriptions,
                "active_summaries": self.active_summaries,
                "cache_bytes": cache_bytes,
            },
        )
        return worker_pb2.CapabilitiesResponse(
            version=__version__,
            yt_dlp_available=shutil.which("yt-dlp") is not None,
            ffmpeg_available=shutil.which("ffmpeg") is not None,
            active_transcriptions=self.active_transcriptions,
            active_summaries=self.active_summaries,
            cache_bytes=cache_bytes,
        )

    async def TestProvider(self, request, context):
        del context
        started = time.monotonic()
        logger.info(
            "provider connectivity test started",
            extra={
                "event": "worker.provider_test.start",
                "profile_kind": request.kind,
                "model": request.provider.model,
            },
        )
        try:
            provider_status = await test_provider(request.kind, request.provider)
            latency_ms = round((time.monotonic() - started) * 1000)
            logger.info(
                "provider connectivity test completed",
                extra={
                    "event": "worker.provider_test.complete",
                    "profile_kind": request.kind,
                    "model": request.provider.model,
                    "http_status": provider_status,
                    "duration_ms": latency_ms,
                },
            )
            return worker_pb2.TestProviderResponse(
                ok=True,
                latency_ms=latency_ms,
                message="模型响应正常",
                provider_http_status=provider_status,
            )
        except ProviderError as exc:
            latency_ms = round((time.monotonic() - started) * 1000)
            logger.warning(
                "provider connectivity test failed",
                extra={
                    "event": "worker.provider_test.failed",
                    "profile_kind": request.kind,
                    "model": request.provider.model,
                    "error_code": exc.code,
                    "http_status": exc.status_code,
                    "duration_ms": latency_ms,
                },
            )
            return worker_pb2.TestProviderResponse(
                ok=False,
                latency_ms=latency_ms,
                message=str(exc),
                error_code=exc.code,
                provider_http_status=exc.status_code,
                provider_error=exc.provider_error,
            )
        except Exception as exc:
            logger.exception(
                "provider connectivity test crashed",
                extra={
                    "event": "worker.provider_test.crashed",
                    "profile_kind": request.kind,
                    "model": request.provider.model,
                    "error_type": type(exc).__name__,
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            return worker_pb2.TestProviderResponse(
                ok=False,
                latency_ms=round((time.monotonic() - started) * 1000),
                message="AI Worker 检测模型时发生内部错误",
                error_code="worker_failure",
            )

    async def Transcribe(self, request, context) -> AsyncIterator[worker_pb2.WorkerEvent]:
        self.active_transcriptions += 1
        succeeded = False
        job_result = "error"
        started = time.monotonic()
        logger.info(
            "transcription job started",
            extra={
                "event": "worker.transcription.start",
                "job_id": request.job_id,
                "bvid": request.bvid,
                "requested_page": request.page,
                "model": request.provider.model,
                "active_transcriptions": self.active_transcriptions,
            },
        )
        try:
            await asyncio.to_thread(cleanup_cache, self.cache_dir, request.failure_cache_ttl_sec, request.cache_max_bytes)
            yield _progress("resolving_video", 2, "正在读取视频信息")
            title, pages = await download_pages(
                self.cache_dir, request.job_id, request.bvid, request.page, dict(request.cookies)
            )
            downloaded_bytes = sum(page.audio_path.stat().st_size for page in pages)
            audio_bytes_metric.add(downloaded_bytes, {"job.kind": "transcription"})
            logger.info(
                "video audio downloaded",
                extra={
                    "event": "worker.transcription.downloaded",
                    "job_id": request.job_id,
                    "bvid": request.bvid,
                    "page_count": len(pages),
                    "audio_bytes": downloaded_bytes,
                },
            )
            yield _progress("downloading_audio", 20, "音频下载完成")
            result_pages: list[worker_pb2.TranscriptPage] = []
            usage: dict[str, Any] = {}
            for page_index, page in enumerate(pages):
                chunks = await split_audio(page)
                chunk_bytes = [chunk.stat().st_size for chunk, _ in chunks]
                logger.info(
                    "audio page split into transcription chunks",
                    extra={
                        "event": "worker.transcription.chunked",
                        "job_id": request.job_id,
                        "bvid": request.bvid,
                        "page": page.page,
                        "duration_ms": page.duration_ms,
                        "chunk_count": len(chunks),
                        "chunk_bytes_total": sum(chunk_bytes),
                        "chunk_bytes_min": min(chunk_bytes),
                        "chunk_bytes_max": max(chunk_bytes),
                    },
                )
                page_segments: list[worker_pb2.TranscriptSegment] = []
                for chunk_index, (chunk, offset_ms) in enumerate(chunks):
                    base = 25 + int(65 * (page_index + chunk_index / max(len(chunks), 1)) / max(len(pages), 1))
                    yield _progress("transcribing", min(base, 90), f"正在转写 P{page.page} 第 {chunk_index + 1}/{len(chunks)} 段")
                    segments, chunk_usage = await transcribe(chunk, request.provider, job_id=request.job_id)
                    _sum_usage(usage, chunk_usage)
                    for segment in segments:
                        page_segments.append(
                            worker_pb2.TranscriptSegment(
                                start_ms=offset_ms + round(segment["start"] * 1000),
                                end_ms=offset_ms + round(segment["end"] * 1000),
                                text=segment["text"],
                            )
                        )
                result_pages.append(
                    worker_pb2.TranscriptPage(
                        page=page.page,
                        cid=page.cid,
                        title=page.title,
                        duration_ms=page.duration_ms,
                        segments=page_segments,
                    )
                )
            yield _progress("persisting_result", 95, "正在保存转写结果")
            yield worker_pb2.WorkerEvent(
                transcription=worker_pb2.TranscriptionResult(
                    bvid=request.bvid, title=title, pages=result_pages, usage=_struct(usage)
                )
            )
            succeeded = True
            job_result = "success"
            logger.info(
                "transcription job completed",
                extra={
                    "event": "worker.transcription.complete",
                    "job_id": request.job_id,
                    "bvid": request.bvid,
                    "page_count": len(result_pages),
                    "segment_count": sum(len(page.segments) for page in result_pages),
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            jobs.add(1, {"job.kind": "transcription", "result": "success"})
        except asyncio.CancelledError:
            job_result = "canceled"
            jobs.add(1, {"job.kind": "transcription", "result": "canceled"})
            logger.warning(
                "transcription job cancelled",
                extra={"event": "worker.transcription.cancelled", "job_id": request.job_id, "bvid": request.bvid},
            )
            raise
        except (DownloadError, ProviderError) as exc:
            code = exc.code if isinstance(exc, ProviderError) else "download_failed"
            jobs.add(1, {"job.kind": "transcription", "result": "error", "error.type": code})
            logger.warning(
                "transcription job failed",
                extra={
                    "event": "worker.transcription.failed",
                    "job_id": request.job_id,
                    "bvid": request.bvid,
                    "error_code": code,
                    "error": str(exc),
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            await context.abort(grpc.StatusCode.FAILED_PRECONDITION, json.dumps({"code": code, "message": str(exc)}))
        except Exception as exc:
            jobs.add(1, {"job.kind": "transcription", "result": "error", "error.type": "worker_failure"})
            logger.exception(
                "transcription job crashed",
                extra={
                    "event": "worker.transcription.crashed",
                    "job_id": request.job_id,
                    "bvid": request.bvid,
                    "error_type": type(exc).__name__,
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            await context.abort(grpc.StatusCode.INTERNAL, json.dumps({"code": "worker_failure", "message": str(exc)}))
        finally:
            self.active_transcriptions -= 1
            job_duration.record(time.monotonic() - started, {"job.kind": "transcription", "result": job_result})
            if succeeded:
                await asyncio.to_thread(shutil.rmtree, self.cache_dir / request.job_id, True)

    async def Summarize(self, request, context) -> AsyncIterator[worker_pb2.WorkerEvent]:
        self.active_summaries += 1
        job_result = "error"
        started = time.monotonic()
        logger.info(
            "summary job started",
            extra={
                "event": "worker.summary.start",
                "job_id": request.job_id,
                "model": request.provider.model,
                "input_chars": len(request.text),
                "active_summaries": self.active_summaries,
            },
        )
        try:
            text = request.text.strip()
            if not text:
                await context.abort(grpc.StatusCode.INVALID_ARGUMENT, json.dumps({"code": "empty_text", "message": "summary text is empty"}))
            chunk_size = max(1000, int(request.provider.context_window_chars * 0.65))
            chunks = _chunks(text, chunk_size)
            usage: dict[str, Any] = {}
            summaries: list[str] = []
            for index, chunk in enumerate(chunks):
                yield _progress("summarizing_chunks", int(75 * index / max(len(chunks), 1)), f"正在总结第 {index + 1}/{len(chunks)} 段")
                prompt = request.chunk_prompt.replace("{{text}}", chunk)
                summary, current_usage = await complete(
                    _messages(request.system_prompt, prompt), request.provider, job_id=request.job_id
                )
                _sum_usage(usage, current_usage)
                summaries.append(summary)
            while len(summaries) > 1:
                yield _progress("reducing_summary", 82, "正在归并分段摘要")
                groups = _chunks("\n\n---\n\n".join(summaries), chunk_size)
                summaries = []
                for group in groups:
                    prompt = request.reduce_prompt.replace("{{summaries}}", group)
                    summary, current_usage = await complete(
                        _messages(request.system_prompt, prompt), request.provider, job_id=request.job_id
                    )
                    _sum_usage(usage, current_usage)
                    summaries.append(summary)
            yield _progress("persisting_result", 95, "正在保存总结结果")
            yield worker_pb2.WorkerEvent(
                summary=worker_pb2.SummaryResult(markdown=summaries[0], usage=_struct(usage))
            )
            logger.info(
                "summary job completed",
                extra={
                    "event": "worker.summary.complete",
                    "job_id": request.job_id,
                    "output_chars": len(summaries[0]),
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            job_result = "success"
            jobs.add(1, {"job.kind": "summary", "result": "success"})
        except asyncio.CancelledError:
            job_result = "canceled"
            jobs.add(1, {"job.kind": "summary", "result": "canceled"})
            logger.warning("summary job cancelled", extra={"event": "worker.summary.cancelled", "job_id": request.job_id})
            raise
        except ProviderError as exc:
            jobs.add(1, {"job.kind": "summary", "result": "error", "error.type": exc.code})
            logger.warning(
                "summary job failed",
                extra={
                    "event": "worker.summary.failed",
                    "job_id": request.job_id,
                    "error_code": exc.code,
                    "error": str(exc),
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            await context.abort(grpc.StatusCode.FAILED_PRECONDITION, json.dumps({"code": exc.code, "message": str(exc)}))
        except Exception as exc:
            jobs.add(1, {"job.kind": "summary", "result": "error", "error.type": "worker_failure"})
            logger.exception(
                "summary job crashed",
                extra={
                    "event": "worker.summary.crashed",
                    "job_id": request.job_id,
                    "error_type": type(exc).__name__,
                    "duration_ms": round((time.monotonic() - started) * 1000),
                },
            )
            await context.abort(grpc.StatusCode.INTERNAL, json.dumps({"code": "worker_failure", "message": str(exc)}))
        finally:
            self.active_summaries -= 1
            job_duration.record(time.monotonic() - started, {"job.kind": "summary", "result": job_result})


def _progress(stage: str, percent: int, message: str) -> worker_pb2.WorkerEvent:
    return worker_pb2.WorkerEvent(progress=worker_pb2.Progress(stage=stage, percent=percent, message=message))


def _messages(system: str, user: str) -> list[dict[str, str]]:
    messages: list[dict[str, str]] = []
    if system.strip():
        messages.append({"role": "system", "content": system})
    messages.append({"role": "user", "content": user})
    return messages


def _chunks(text: str, limit: int) -> list[str]:
    chunks: list[str] = []
    current = ""
    for paragraph in text.splitlines():
        paragraph = paragraph.strip()
        if not paragraph:
            continue
        while len(paragraph) > limit:
            if current:
                chunks.append(current)
                current = ""
            chunks.append(paragraph[:limit])
            paragraph = paragraph[limit:]
        candidate = f"{current}\n{paragraph}".strip()
        if len(candidate) > limit and current:
            chunks.append(current)
            current = paragraph
        else:
            current = candidate
    if current:
        chunks.append(current)
    return chunks or [text[:limit]]


async def serve(socket_path: Path, cache_dir: Path) -> None:
    socket_path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    socket_path.unlink(missing_ok=True)
    server = grpc.aio.server(
        interceptors=(aio_server_interceptor(filter_=filters.negate(filters.method_name("GetCapabilities"))),),
        options=(("grpc.max_receive_message_length", 16 << 20),),
    )
    worker_pb2_grpc.add_AIWorkerServicer_to_server(AIWorker(cache_dir), server)
    server.add_insecure_port(f"unix:{socket_path}")
    await server.start()
    os.chmod(socket_path, 0o600)
    logger.info(
        "AI worker started",
        extra={
            "event": "worker.start",
            "version": __version__,
            "socket": str(socket_path),
            "cache_dir": str(cache_dir),
            "yt_dlp_available": shutil.which("yt-dlp") is not None,
            "ffmpeg_available": shutil.which("ffmpeg") is not None,
        },
    )
    try:
        await server.wait_for_termination()
    finally:
        logger.info("AI worker stopping", extra={"event": "worker.stop"})
        await server.stop(10)
        socket_path.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", default=os.environ.get("BILI_NOTIFY_AI_WORKER_SOCKET", "/run/bili-notify/ai-worker.sock"))
    parser.add_argument("--cache-dir", default=os.environ.get("BILI_NOTIFY_AI_CACHE_DIR", "/cache"))
    args = parser.parse_args()
    telemetry_runtime = configure_telemetry()
    configure_logging(telemetry_runtime.logging_handler)
    try:
        asyncio.run(serve(Path(args.socket), Path(args.cache_dir)))
    finally:
        telemetry_runtime.shutdown()


if __name__ == "__main__":
    main()

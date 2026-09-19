import json
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


def gemini_config() -> SimpleNamespace:
    current = config()
    current.base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
    current.model = "gemini-3.5-transcribe"
    return current


def dashscope_config() -> SimpleNamespace:
    current = config()
    current.base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
    current.model = "qwen3-omni-flash"
    return current


def install_transport(monkeypatch: pytest.MonkeyPatch, handler: object) -> None:
    real_async_client = httpx.AsyncClient

    def client_factory(**kwargs: object) -> httpx.AsyncClient:
        options = dict(kwargs)
        options.pop("transport", None)
        return real_async_client(transport=httpx.MockTransport(handler), **options)  # type: ignore[arg-type]

    monkeypatch.setattr(provider.httpx, "AsyncClient", client_factory)


@pytest.mark.parametrize(
    ("base_url", "expected"),
    [
        ("https://openrouter.ai/api/v1", provider.PROTOCOL_OPENAI_AUDIO),
        ("https://generativelanguage.googleapis.com/v1beta/openai", provider.PROTOCOL_GEMINI),
        ("https://generativelanguage.googleapis.com", provider.PROTOCOL_GEMINI),
        ("https://dashscope.aliyuncs.com/compatible-mode/v1", provider.PROTOCOL_CHAT_AUDIO),
        ("https://dashscope-intl.aliyuncs.com/compatible-mode/v1", provider.PROTOCOL_CHAT_AUDIO),
        ("https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", provider.PROTOCOL_CHAT_AUDIO),
        ("https://oss.aliyuncs.com/v1", provider.PROTOCOL_OPENAI_AUDIO),
    ],
)
def test_transcription_protocol_uses_base_url_host(base_url: str, expected: str) -> None:
    assert provider.transcription_protocol(base_url) == expected


@pytest.mark.parametrize(
    ("base_url", "expected"),
    [
        ("https://openrouter.ai/api/v1", 600_000),
        ("https://generativelanguage.googleapis.com/v1beta/openai", 600_000),
        ("https://dashscope.aliyuncs.com/compatible-mode/v1", 150_000),
    ],
)
def test_transcription_chunk_duration_matches_protocol(base_url: str, expected: int) -> None:
    assert provider.transcription_chunk_duration_ms(base_url) == expected


@pytest.mark.parametrize(
    ("language", "expected"),
    [("zh", ["cmn-Hans-CN"]), ("en", ["en-US"]), ("", None), ("tlh", None)],
)
def test_gemini_language_codes_only_map_known_values(language: str, expected: list[str] | None) -> None:
    assert provider._gemini_language_codes(language) == expected


@pytest.mark.parametrize(
    ("left", "right", "expected"),
    [("Hello", "world", "Hello world"), ("你", "好", "你好"), ("Hello ", "there", "Hello there")],
)
def test_join_transcript_spacing(left: str, right: str, expected: str) -> None:
    assert provider._join_transcript(left, right) == expected


def test_group_words_splits_on_pause_and_sentence_end() -> None:
    words = [
        {"start": 0.1, "end": 0.45, "text": "Hello"},
        {"start": 0.5, "end": 0.85, "text": "world."},
        {"start": 2.0, "end": 2.4, "text": "Next"},
    ]

    assert provider._group_words(words) == [
        {"start": 0.1, "end": 0.85, "text": "Hello world."},
        {"start": 2.0, "end": 2.4, "text": "Next"},
    ]


@pytest.mark.asyncio
async def test_gemini_probe_uses_interactions_inline_audio(monkeypatch: pytest.MonkeyPatch) -> None:
    requests: list[httpx.Request] = []

    def handle(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, json={"status": "completed", "output_text": ""})

    install_transport(monkeypatch, handle)

    status = await provider.test_provider("transcription", gemini_config())

    assert status == 200
    assert len(requests) == 1
    request = requests[0]
    assert str(request.url) == "https://generativelanguage.googleapis.com/v1beta/interactions"
    assert request.headers["x-goog-api-key"] == "secret"
    assert request.headers["authorization"] == "Bearer secret"
    body = json.loads(request.content)
    assert body["model"] == "gemini-3.5-transcribe"
    assert body["input"][0]["type"] == "audio"
    assert body["input"][0]["mime_type"] == "audio/wav"
    assert body["input"][0]["data"]
    assert "uri" not in body["input"][0]
    assert body["generation_config"]["transcription_config"]["language_codes"] == ["cmn-Hans-CN"]
    assert "mode" not in body["generation_config"]["transcription_config"]


@pytest.mark.asyncio
async def test_gemini_probe_classifies_http_404(monkeypatch: pytest.MonkeyPatch) -> None:
    install_transport(monkeypatch, lambda _request: httpx.Response(404, json={"error": {"message": "not found"}}))

    with pytest.raises(provider.ProviderError) as caught:
        await provider.test_provider("transcription", gemini_config())

    assert caught.value.code == "provider_model_not_found"
    assert caught.value.status_code == 404


@pytest.mark.asyncio
async def test_gemini_transcribe_uploads_then_requests_word_timestamps(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio-bytes")
    requests: list[httpx.Request] = []

    def handle(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        path = request.url.path
        if request.method == "POST" and path.endswith("/upload/v1beta/files"):
            if request.url.params.get("upload_id") == "1":
                return httpx.Response(
                    200,
                    json={
                        "file": {
                            "name": "files/abc",
                            "uri": "https://generativelanguage.googleapis.com/v1beta/files/abc",
                            "state": "ACTIVE",
                        }
                    },
                )
            return httpx.Response(
                200,
                headers={"x-goog-upload-url": "https://generativelanguage.googleapis.com/upload/v1beta/files?upload_id=1"},
                json={},
            )
        if request.method == "POST" and path.endswith("/v1beta/interactions"):
            return httpx.Response(
                200,
                json={
                    "status": "completed",
                    "output_text": "Hello world",
                    "steps": [
                        {
                            "type": "model_output",
                            "content": [
                                {
                                    "type": "text",
                                    "text": "Hello world",
                                    "annotations": [
                                        {
                                            "type": "word_info",
                                            "text": "Hello",
                                            "start_offset": "0.100s",
                                            "end_offset": "0.450s",
                                        },
                                        {
                                            "type": "word_info",
                                            "text": "world",
                                            "start_offset": "0.500s",
                                            "end_offset": "0.850s",
                                        },
                                    ],
                                }
                            ],
                        }
                    ],
                    "usage": {"total_tokens": 12},
                },
            )
        if request.method == "DELETE" and path.endswith("/v1beta/files/abc"):
            return httpx.Response(200, json={})
        return httpx.Response(500, json={"error": {"message": f"{request.method} {request.url}"}})

    install_transport(monkeypatch, handle)

    segments, usage = await provider.transcribe(audio, gemini_config(), job_id="job-1")

    assert segments == [{"start": 0.1, "end": 0.85, "text": "Hello world"}]
    assert usage == {"total_tokens": 12}
    assert [request.method for request in requests] == ["POST", "POST", "POST", "DELETE"]
    interaction = json.loads(requests[2].content)
    assert interaction["input"][0]["uri"] == "https://generativelanguage.googleapis.com/v1beta/files/abc"
    assert interaction["generation_config"]["transcription_config"]["mode"] == {
        "type": "verbatim",
        "timestamp_granularities": ["word"],
    }
    assert "custom_vocabulary" not in interaction["generation_config"]["transcription_config"]


@pytest.mark.asyncio
async def test_gemini_transcribe_falls_back_to_output_text_without_annotations(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio-bytes")

    def handle(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if request.method == "POST" and path.endswith("/upload/v1beta/files"):
            if request.url.params.get("upload_id") == "1":
                return httpx.Response(200, json={"file": {"name": "files/abc", "uri": "files/abc", "state": "ACTIVE"}})
            return httpx.Response(
                200,
                headers={"x-goog-upload-url": "https://generativelanguage.googleapis.com/upload/v1beta/files?upload_id=1"},
                json={},
            )
        if request.method == "POST" and path.endswith("/v1beta/interactions"):
            return httpx.Response(200, json={"output_text": "spoken words", "duration": 12.5})
        if request.method == "DELETE":
            return httpx.Response(200, json={})
        return httpx.Response(500, json={"error": {"message": "unexpected"}})

    install_transport(monkeypatch, handle)

    segments, _usage = await provider.transcribe(audio, gemini_config(), job_id="job-1")

    assert segments == [{"start": 0.0, "end": 12.5, "text": "spoken words"}]


@pytest.mark.asyncio
async def test_gemini_rejects_unexpected_upload_host(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio-bytes")
    install_transport(
        monkeypatch,
        lambda _request: httpx.Response(
            200,
            headers={"x-goog-upload-url": "https://evil.example/upload"},
            json={},
        ),
    )

    with pytest.raises(provider.ProviderError) as caught:
        await provider.transcribe(audio, gemini_config(), job_id="job-1")

    assert caught.value.code == "provider_invalid_response"


@pytest.mark.asyncio
async def test_chat_audio_probe_posts_data_url(monkeypatch: pytest.MonkeyPatch) -> None:
    requests: list[httpx.Request] = []

    def handle(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, json={"choices": [{"message": {"content": "OK"}}]})

    install_transport(monkeypatch, handle)

    status = await provider.test_provider("transcription", dashscope_config())

    assert status == 200
    request = requests[0]
    assert str(request.url).endswith("/compatible-mode/v1/chat/completions")
    body = json.loads(request.content)
    assert body["model"] == "qwen3-omni-flash"
    assert body["enable_thinking"] is False
    assert body["temperature"] == 0
    audio = body["messages"][0]["content"][0]["input_audio"]["data"]
    assert audio.startswith("data:audio/wav;base64,")
    assert body["messages"][0]["content"][1]["text"] == provider.PROBE_CHAT_AUDIO_PROMPT


@pytest.mark.asyncio
async def test_chat_audio_transcribe_requires_content(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    audio.write_bytes(b"audio-bytes")
    install_transport(
        monkeypatch,
        lambda _request: httpx.Response(200, json={"choices": [{"message": {"content": "   "}}]}),
    )

    with pytest.raises(provider.ProviderError) as caught:
        await provider.transcribe(audio, dashscope_config(), job_id="job-1")

    assert caught.value.code == "provider_invalid_response"


@pytest.mark.asyncio
async def test_chat_audio_transcribe_returns_single_duration_segment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    audio = tmp_path / "audio.wav"
    with wave.open(str(audio), "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(8_000)
        wav.writeframes(b"\x00\x00" * 8_000)

    def handle(request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content)
        assert body["enable_thinking"] is False
        assert body["messages"][0]["content"][1]["text"] == provider.DEFAULT_CHAT_AUDIO_PROMPT
        return httpx.Response(
            200,
            json={"choices": [{"message": {"content": "转写结果"}}], "usage": {"total_tokens": 9}},
        )

    install_transport(monkeypatch, handle)

    segments, usage = await provider.transcribe(audio, dashscope_config(), job_id="job-1")

    assert len(segments) == 1
    assert segments[0]["text"] == "转写结果"
    assert segments[0]["start"] == 0.0
    assert segments[0]["end"] == pytest.approx(1.0)
    assert usage == {"total_tokens": 9}

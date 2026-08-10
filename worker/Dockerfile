# syntax=docker/dockerfile:1.7
FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app/worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY worker/requirements.lock /app/worker/requirements.lock
RUN pip install --no-cache-dir -r /app/worker/requirements.lock
COPY worker /app/worker
RUN mkdir -p /cache /run/bili-notify && chown -R 65532:65532 /cache /run/bili-notify /app
USER 65532:65532
VOLUME ["/cache", "/run/bili-notify"]
ENTRYPOINT ["python", "-m", "bili_ai_worker.server"]

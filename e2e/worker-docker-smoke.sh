#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: e2e/worker-docker-smoke.sh IMAGE}
run_id=${GITHUB_RUN_ID:-local}
container="bili-notify-worker-smoke-${run_id}-$$"
volume="bili-notify-worker-smoke-socket-${run_id}-$$"

cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  docker volume rm "${volume}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

[[ $(docker image inspect --format '{{.Config.User}}' "${image}") == "65532:65532" ]]
[[ $(docker image inspect --format '{{json .Config.Entrypoint}}' "${image}") == '["python","-m","bili_ai_worker.server"]' ]]

docker volume create "${volume}" >/dev/null
docker run --detach \
  --name "${container}" \
  --read-only \
  --tmpfs /tmp:size=32m,mode=1777 \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  --volume "${volume}:/run/bili-notify" \
  "${image}" >/dev/null

for _ in $(seq 1 30); do
  if docker run --rm \
    --volume "${volume}:/run/bili-notify" \
    --entrypoint python \
    "${image}" \
    -c 'import grpc; from ai.v1 import worker_pb2, worker_pb2_grpc; channel = grpc.insecure_channel("unix:/run/bili-notify/ai-worker.sock"); capabilities = worker_pb2_grpc.AIWorkerStub(channel).GetCapabilities(worker_pb2.CapabilitiesRequest(), timeout=3); assert capabilities.yt_dlp_available and capabilities.ffmpeg_available' >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done

docker logs "${container}" >&2
exit 1

#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: e2e/docker-smoke.sh IMAGE}
run_id=${GITHUB_RUN_ID:-local}
container="bili-notify-smoke-${run_id}-$$"
volume="bili-notify-smoke-data-${run_id}-$$"

cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  docker volume rm "${volume}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_for_health() {
  for _ in $(seq 1 60); do
    if docker exec "${container}" /bili-notify healthcheck >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  docker logs "${container}" >&2
  return 1
}

wait_for_setup_code() {
  local code
  for _ in $(seq 1 30); do
    code=$(docker logs "${container}" 2>&1 | sed -n 's/.*"setup_code":"\([A-Z0-9]*\)".*/\1/p' | tail -1)
    if [[ -n "${code}" ]]; then
      printf '%s' "${code}"
      return 0
    fi
    sleep 1
  done
  docker logs "${container}" >&2
  return 1
}

wait_for_admin() {
  for _ in $(seq 1 30); do
    if curl --fail --silent --insecure "${admin_url}/api/v1/session" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  docker logs "${container}" >&2
  return 1
}

[[ $(docker image inspect --format '{{.Config.User}}' "${image}") == "65532:65532" ]]
docker volume create "${volume}" >/dev/null
docker run --detach \
  --name "${container}" \
  --read-only \
  --tmpfs /tmp:size=16m,mode=1777 \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  --volume "${volume}:/data" \
  --publish 127.0.0.1::8443 \
  "${image}" >/dev/null

wait_for_health
admin_port=$(docker port "${container}" 8443/tcp | sed -n '1s/.*://p')
[[ -n "${admin_port}" ]]
admin_url="https://127.0.0.1:${admin_port}"
wait_for_admin
curl --fail --silent --show-error --insecure "${admin_url}/" | grep -q '<div id="root"></div>'
curl --fail --silent --show-error --insecure "${admin_url}/api/v1/session" | grep -q '"setup_required":true'

setup_code=$(wait_for_setup_code)
curl --fail --silent --show-error --insecure \
  --header 'Content-Type: application/json' \
  --data "{\"setup_code\":\"${setup_code}\",\"password\":\"correct horse battery staple\"}" \
  "${admin_url}/api/v1/setup" >/dev/null

docker stop --time 20 "${container}" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "${container}") == "0" ]]
docker start "${container}" >/dev/null
wait_for_health
admin_port=$(docker port "${container}" 8443/tcp | sed -n '1s/.*://p')
[[ -n "${admin_port}" ]]
admin_url="https://127.0.0.1:${admin_port}"
wait_for_admin
curl --fail --silent --show-error --insecure "${admin_url}/api/v1/session" | grep -q '"setup_required":false'

docker stop --time 20 "${container}" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "${container}") == "0" ]]

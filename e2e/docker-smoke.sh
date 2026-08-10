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

# All probes run via docker exec inside the service container. The CI self-hosted
# runner is itself a container with docker.sock access; publishing 127.0.0.1:port
# binds on the host loopback and is not reachable from the runner container.
wait_for_probe() {
  local label=$1
  shift
  for _ in $(seq 1 60); do
    if docker exec "${container}" /bili-notify healthcheck "$@"; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for ${label}" >&2
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

[[ $(docker image inspect --format '{{.Config.User}}' "${image}") == "65532:65532" ]]
[[ $(docker image inspect --format '{{json .Config.Entrypoint}}' "${image}") == '["/bili-notify"]' ]]
[[ $(docker image inspect --format '{{json .Config.Cmd}}' "${image}") == '["serve"]' ]]
[[ $(docker image inspect --format '{{json .Config.Healthcheck.Test}}' "${image}") == '["CMD","/bili-notify","healthcheck"]' ]]
docker volume create "${volume}" >/dev/null
docker run --detach \
  --name "${container}" \
  --read-only \
  --tmpfs /tmp:size=16m,mode=1777 \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  --volume "${volume}:/data" \
  "${image}" >/dev/null

wait_for_probe "liveness" --url http://127.0.0.1:9090/healthz
wait_for_probe "admin session" --insecure --url https://127.0.0.1:8443/api/v3/session --contains '"setup_required":true'
wait_for_probe "admin UI" --insecure --url https://127.0.0.1:8443/ --contains '<div id="root"></div>'

[[ $(docker inspect --format '{{.Config.User}}' "${container}") == "65532:65532" ]]
[[ $(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "${container}") == "true" ]]

setup_code=$(wait_for_setup_code)
docker exec "${container}" /bili-notify healthcheck \
  --insecure \
  --method POST \
  --url https://127.0.0.1:8443/api/v3/setup \
  --body "{\"setup_code\":\"${setup_code}\",\"password\":\"correct horse battery staple\"}"

docker stop --time 20 "${container}" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "${container}") == "0" ]]
docker start "${container}" >/dev/null
wait_for_probe "liveness after restart" --url http://127.0.0.1:9090/healthz
wait_for_probe "admin session after restart" --insecure --url https://127.0.0.1:8443/api/v3/session --contains '"setup_required":false'

docker stop --time 20 "${container}" >/dev/null
[[ $(docker inspect --format '{{.State.ExitCode}}' "${container}") == "0" ]]

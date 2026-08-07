#!/bin/sh
set -eu

compose_file=${1:-compose.full.yaml}
project_name=${COMPOSE_PROJECT_NAME:-bili-notify-observability}
compose_cmd=${COMPOSE_CMD:-docker-compose}

collector_id=$($compose_cmd -p "$project_name" -f "$compose_file" ps -q otel-collector)
if [ -z "$collector_id" ]; then
  echo "otel-collector is not running for project $project_name" >&2
  exit 1
fi
network_name=$(docker inspect "$collector_id" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}')

request() {
  docker run --rm --network "$network_name" curlimages/curl:8.16.0 -fsS "$@"
}

wait_for() {
  name=$1
  shift
  attempts=45
  until "$@"; do
    attempts=$((attempts - 1))
    if [ "$attempts" -eq 0 ]; then
      echo "timed out waiting for $name" >&2
      return 1
    fi
    sleep 2
  done
}

collector_ready() {
  request http://otel-collector:8888/metrics | grep -q 'otelcol_process_uptime'
}

application_metric_received() {
  request 'http://prometheus:9090/api/v1/query?query=bili_notify_config_poll_interval' | grep -q '"result":\[{'
}

application_log_received() {
  request --get --data-urlencode 'query={service_name="bili-notify"}' 'http://loki:3100/loki/api/v1/query_range' | grep -q '"result":\[{'
}

application_trace_received() {
  request --get --data-urlencode 'tags=service.name=bili-notify' 'http://tempo:3200/api/search' | grep -q '"traces":\[{'
}

grafana_ready() {
  request http://grafana:3000/api/health | grep -Eq '"database"[[:space:]]*:[[:space:]]*"ok"'
}

wait_for "Collector" collector_ready
request -k https://bili-notify:8443/api/v1/session >/dev/null
wait_for "application metrics in Prometheus" application_metric_received
wait_for "application logs in Loki" application_log_received
wait_for "application traces in Tempo" application_trace_received
wait_for "Grafana" grafana_ready

echo "observability smoke passed"

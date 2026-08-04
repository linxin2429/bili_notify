# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Single-instance Go service that polls Bilibili UP dynamics via logged-in web APIs and delivers notifications to email (SMTP), Microsoft Graph (OAuth device code), DingTalk, Feishu, and WeCom robots. Runtime config (UPs, channels, Bilibili session) is managed through a TLS admin console and persisted in bbolt. Secrets (cookies, tokens, webhooks) are AES-256-GCM encrypted with a file-based master key.

Bilibili has no stable public push API for arbitrary UPs. This uses unofficial web endpoints and does **not** implement captcha solving, proxy pools, or other risk-control evasion. Unknown dynamic schemas must fail loudly, not be guessed.

Detailed product/design constraints: `docs/requirements-and-design.md`. Repo conventions also live in `AGENTS.md`.

## Commands

Requires Go 1.26+.

```bash
go build ./...
go test ./...
go test -race ./...
go vet ./...
go test ./service -run TestName -count=1   # single package / test
go run . --help
go run . serve                             # needs secret files + paths configured
gofmt -w <files>
```

Docker (production-like scratch image, nonroot UID 65532):

```bash
docker compose up -d --build
docker compose logs -f bili-notify
docker compose exec bili-notify /bili-notify healthcheck
docker compose run --rm bili-notify --help
docker compose run --rm bili-notify admin hash-password
```

Master-key rotation (service must be stopped):

```bash
docker compose stop bili-notify
docker compose run --rm -v ./secrets/new-master-key:/run/secrets/new-master-key:ro \
  bili-notify rekey --new-key-file /run/secrets/new-master-key
```

CI (`.github/workflows/ci.yml`): `go test`, `go test -race`, `go vet`, `govulncheck`, `docker build`.

Never commit material under `secrets/`, bbolt DBs, cookies, OAuth tokens, webhooks, or TLS private keys.

## Architecture

```
main.go → cmd/ (Cobra CLI + Viper/BILI_NOTIFY_* env)
            └─ app.Run  wires vault, state, bilibili client, service.Engine, web.Server
```

| Package | Responsibility |
| --- | --- |
| `cmd/` | CLI only: `serve`, `admin hash-password`, `healthcheck`, `rekey`. Startup flags/env defaults. |
| `app/` | Composition root: validate config, open store, build engine + dual HTTP servers. |
| `config/` | Immutable **startup** settings only (paths, poll rate, log level). Runtime data is in bbolt. |
| `model/` | Shared domain types + validation (UP, Channel, Dynamic, Delivery, BiliSession). |
| `bilibili/` | Web API client: QR login, session validate, space dynamics feed, strict dynamic parsing. |
| `state/` | bbolt store: UPs, channels, encrypted session/secrets, seen dynamics, Outbox deliveries. |
| `vault/` | AES-256-GCM seal/open with per-record nonce; AAD = bucket+key. |
| `service/` | Engine: poll loop, Outbox delivery loop, auth loop, QR/Microsoft login sessions, metrics, system alerts. |
| `notify/` | Channel adapters (`Sender` interface): SMTP, Microsoft Graph, DingTalk/Feishu/WeCom robots. |
| `web/` | TLS 1.3 admin UI (`index.html` + JSON API) on `:8443`; observe (`/healthz`, `/readyz`, `/metrics`) on `:9090`. |

### Core runtime flow

1. **Collect** (`service.Engine.collectLoop`): every ~`poll_interval` (default 30s), rate-limited (2 rps, 4 concurrency) fetch each enabled UP's dynamics. Paginate up to 10 pages until a known dynamic ID; more than 10 pages is a state gap — stop that UP without committing (no silent loss).
2. **Baseline vs notify**: first successful poll for a new UP only records seen IDs (`BaselineReady`); no historical notifications. Later polls create Outbox tasks.
3. **Atomic Outbox** (`state.Store.RecordDynamics`): in one bbolt write txn, mark dynamics seen and enqueue one delivery per enabled channel (key = dynamic ID + channel ID).
4. **Deliver** (`deliveryLoop`): due tasks are sent via `notify.Sender`. Success deletes the task. Transient failures retry with jittered backoff (5s → 30s → 2m → 10m → max 1h). Permanent/auth/config errors **block** the delivery until the channel is updated.
5. **Auth**: invalid Bilibili session fails readiness and pauses collection; Outbox delivery continues. Microsoft access tokens refresh automatically and persist via channel settings updates.

### Important invariants

- Startup secrets (master key, admin password hash, TLS cert/key paths) are **not** configurable via the web UI.
- Channel secret fields are write-only; reads return masks; an update that sends a mask keeps the old value.
- Disabling a channel stops new tasks and pauses existing ones; re-enabling does **not** backfill the gap. Channels with pending deliveries cannot be deleted.
- Deleting a UP clears its seen state; re-adding re-baselines.
- Logs/metrics must never include cookies, OAuth tokens, SMTP passwords, webhooks, or dynamic body text.
- Unknown Bilibili dynamic types/fields → schema error; do not invent a generic fallback notification.
- Single process: bbolt file lock enforces one writer; no multi-instance HA.

### Config surface

- Env prefix: `BILI_NOTIFY_` (dots/dashes → underscores), e.g. `BILI_NOTIFY_LOG_LEVEL`, `BILI_NOTIFY_POLL_INTERVAL`.
- Defaults in `cmd/root.go` (`data_path`, `admin_addr`, `observe_addr`, secret paths under `/run/secrets`, poll 30s, rate 2, concurrency 4).
- Local Docker expects host `./secrets` mounted read-only at `/run/secrets`, with POSIX ACL granting UID 65532 read access (see README).

When changing behavior that affects reliability, security, polling, or channel contracts, keep `docs/requirements-and-design.md` aligned.

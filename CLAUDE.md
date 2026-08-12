# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Single-instance Go service that polls Bilibili UP dynamics via logged-in web APIs and delivers notifications to email (SMTP), Microsoft Graph (OAuth device code), DingTalk, Feishu, and WeCom robots. Runtime config (UPs, channels, Bilibili session), Outbox, and content archive live in a single SQLite `data.db` (GORM + goose SQL migrations on every startup). Secrets (cookies, tokens, webhooks) are AES-256-GCM encrypted with a file-based master key.

Bilibili has no stable public push API for arbitrary UPs. This uses unofficial web endpoints and does **not** implement captcha solving, proxy pools, or other risk-control evasion. Unknown dynamic schemas must fail loudly, not be guessed.

Detailed product/design constraints: `docs/requirements-and-design.md`. Repo conventions also live in `AGENTS.md`.

## Commands

Requires the Go version declared in `go.mod`.

```bash
make build
make docker-build
make test
make test-race
make vet
make test GO_PACKAGES=./service GO_TEST_FLAGS='-run TestName -count=1'
make run ARGS=--help
make run                                    # needs secret files + paths configured
make fmt
```

## Testing principles

When writing or changing Go tests, follow these rules (also in `AGENTS.md`):

1. **Table-driven**: every multi-case scenario uses `tests := []struct { name string; ... }{...}` and isolates cases with `t.Run(tt.name, func(t *testing.T) { ... })`.
2. **`t.Parallel()`**: call it inside each subtest so cases run concurrently.
3. **Testify**: import both `assert` and `require`.
   - `require.*` for setup, errors, and preconditions later logic depends on (hard stop).
   - `assert.*` for multi-field / independent checks (soft, keep going).
4. **`t.Cleanup` only**: for temp files, mock servers, DB handles, or goroutine shutdown — **never** `defer` in tests.
5. **Cover paths**: happy path, edge cases (empty/nil/zero/extremes), and every `error` branch. Match errors with `errors.Is` (or typed equality), not just `err != nil`.
6. **No flaky waits**: never bare `time.Sleep` for concurrency; use channels or `assert.Eventually` / `require.Eventually`.
7. **Fuzz (optional)**: for string transform, ser/de (JSON/YAML/XML), protocol pack/unpack, or core pure algorithms, add a native `Fuzz*` test.

Docker (production-like scratch image, nonroot UID 65532):

```bash
make compose-up
make compose-logs
make compose-healthcheck
make compose-run ARGS=--help
make compose-run ARGS='admin hash-password'
```

Master-key rotation (service must be stopped):

```bash
make compose-stop
make compose-run \
  COMPOSE_RUN_FLAGS='-v ./secrets/new-master-key:/run/secrets/new-master-key:ro' \
  ARGS='rekey --new-key-file /run/secrets/new-master-key'
```

CI (`.github/workflows/ci.yml`) prefers the Linux self-hosted runner, falls back to the macOS self-hosted runner, and then GitHub-hosted Ubuntu:
- `test`: runner-specific Make and tool worker budgets run the local build, frontend checks, isolated Playwright scenarios, combined Go coverage/race, vet, and govulncheck in parallel.
- `docker-smoke`: `make docker-smoke` builds the multi-stage image with CI metadata and validates startup, health, and restart.
- `docker`: only `v*` tags push `dengxinlin/bili-notify` (`X.Y.Z`, `X.Y`, `X`, `latest`). Secrets: `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.

Never commit material under `secrets/`, SQLite DBs (`data.db`), cookies, OAuth tokens, webhooks, or TLS private keys.

## Commit messages

Enable the repository hook once per clone with `git config core.hooksPath .githooks`. Every authored commit subject must follow Conventional Commits:

```text
<type>[(scope)][!]: <description>
```

Allowed types are `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, and `revert`. The optional scope must start with a lowercase ASCII letter or digit and may contain lowercase letters, digits, `.`, `_`, `/`, or `-`. Use `!` for a breaking change. The description must be non-empty, concise, and imperative. Examples: `feat: add dynamic filtering`, `fix(notify): retry timed-out webhooks`, and `refactor(state)!: replace the storage schema`.

Every authored commit must also include a detailed body with at least two non-empty lines and at least 30 non-whitespace characters in total. Git-generated `Merge ...` and `Revert "..."` messages are exempt from both checks.

## Architecture

```
main.go → cmd/ (Cobra CLI + Viper/BILI_NOTIFY_* env)
            └─ app.Run  wires vault, state, bilibili client, service.Engine, web.Server
```

| Package | Responsibility |
| --- | --- |
| `cmd/` | CLI only: `serve`, `admin hash-password`, `healthcheck`, `rekey`. Startup flags/env defaults. |
| `app/` | Composition root: validate config, open store, build engine + dual HTTP servers. |
| `config/` | Startup settings: paths, listen addrs, log level are process-immutable. Poll interval / request rate / concurrency / comment knobs are **first-run defaults** only; after seed they live in SQLite and hot-reload via admin UI. |
| `model/` | Shared domain facts and validation (Source, Content, CommentNode, Channel, Delivery, accounts, AI jobs). |
| `bilibili/` | Web API client: QR login, session validate, space dynamics feed, strict dynamic parsing. |
| `zsxq/` | Official Knowledge Planet MCP client, credential lifecycle, dynamic/backfill worker, and comment-tree worker. |
| `sources/` | Source administration use cases and post-commit invalidation callbacks. |
| `state/` | Single SQLite adapter (`data.db`): sources, contents, comment nodes, seen items, the only Outbox, encrypted settings/accounts, AI jobs, cleanup tasks; goose migrations on open. |
| `media/` | Safe attachment localization plus persistent, idempotent cleanup worker. |
| `vault/` | AES-256-GCM seal/open with per-record nonce; AAD = table+key. |
| `service/` | Engine: poll loop, Outbox delivery loop, auth loop, QR/Microsoft login sessions, metrics, system alerts. |
| `notify/` | Channel adapters (`Sender` interface): SMTP, Microsoft Graph, DingTalk/Feishu/WeCom robots. |
| `web/` | TLS 1.3 admin UI (`index.html` + JSON API) on `:8443`; observe (`/healthz`, `/readyz`) on `:9090`; OTLP metrics are exported through the Collector. |

### Core runtime flow

1. **Collect** (`service.Engine.collectLoop`): every ~runtime `poll_interval` (seed default 30s), rate-limited (seed default 2 rps, 4 concurrency) fetch each enabled UP's dynamics. These collector knobs are stored in SQLite and hot-reloaded from the admin UI. Paginate up to 10 pages until a known dynamic ID; more than 10 pages is a state gap — stop that UP without committing (no silent loss). Discovered commentable contents refresh each UP's recent-N comment targets.
2. **Baseline vs notify**: first successful poll for a new UP only records seen IDs (`BaselineReady`); no historical notifications. Later polls create Outbox tasks.
3. **Atomic Outbox** (`state.Store.RecordDynamics` / `SyncCommentTree` / `ArchiveContentAndEnqueue`): in one SQLite transaction, archive the unified content or comment tree, mark `seen_items`, update sync state, and enqueue an immutable snapshot for every channel enabled at commit time. Zero channels never suppresses collection.
4. **Comment replies** (`service.Engine.commentLoop`): slower batch (seed default 120s) scans tracked content via `/x/v2/reply` + `/x/v2/reply/reply`, keeps only UP-authored replies, expands root→trigger thread, baselines on first success per target.
5. **Deliver** (`deliveryLoop`): due tasks are sent via `notify.Sender`. Success deletes the task. Transient failures retry with jittered backoff (5s → 30s → 2m → 10m → max 1h). Permanent/auth/config errors **block** the delivery until the channel is updated.
6. **Auth**: invalid Bilibili session fails readiness and pauses collection; Outbox delivery continues. Microsoft access tokens refresh automatically and persist via channel settings updates.

### Important invariants

- Startup secrets (master key, admin password hash, TLS cert/key paths) are **not** configurable via the web UI.
- Channel secret fields are write-only; reads return only `configured_secrets`; omitting a secret on update preserves it.
- Disabling a channel stops new tasks and pauses existing ones; re-enabling does **not** backfill the gap. Channels with pending deliveries cannot be deleted.
- Deleting a source clears indexed Outbox/seen/content state and transactionally schedules persistent media cleanup; re-adding re-baselines.
- Logs/metrics must never include cookies, OAuth tokens, SMTP passwords, webhooks, or dynamic body text.
- Unknown Bilibili dynamic types/fields → schema error; do not invent a generic fallback notification.
- Single process: SQLite single-writer (`MaxOpenConns=1` + WAL); no multi-instance HA.
- Legacy dual-store volumes (`state.db` / `content.db`) refuse start; only fresh `data.db` is supported.
- Schema v10 is an irreversible transactional conversion from v0.4.5; operators must stop the process and back up `data.db`, WAL/SHM files when present, and `master.key` before upgrade.
- The browser API is `/api/v4` only; `/api/v3` is intentionally unregistered.

### Config surface

- Env prefix: `BILI_NOTIFY_` (dots/dashes → underscores), e.g. `BILI_NOTIFY_LOG_LEVEL`, `BILI_NOTIFY_POLL_INTERVAL`.
- Defaults in `cmd/root.go` (`data_dir`, `admin_addr`, `observe_addr`, poll 30s / rate 2 / concurrency 4 as **seed** defaults for a new data directory).
- Local Docker expects host `./secrets` mounted read-only at `/run/secrets`, with POSIX ACL granting UID 65532 read access (see README).

When changing behavior that affects reliability, security, polling, or channel contracts, keep `docs/requirements-and-design.md` aligned.

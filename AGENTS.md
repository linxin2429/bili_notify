# Repository Guidelines

## Project Structure & Module Organization

`main.go` starts the Cobra CLI; command definitions live in `cmd/`, and `app/` wires the service together. Domain packages are organized by responsibility: `bilibili/` calls and parses Bilibili APIs, `service/` runs polling and delivery workflows, `notify/` implements notification channels, `state/` persists data in bbolt, `vault/` encrypts secrets, `web/` serves the TLS administration UI, and `model/` holds shared types. Startup configuration belongs in `config/`. Keep architectural decisions synchronized with `docs/requirements-and-design.md`. Tests sit beside their packages as `*_test.go`; browser assets currently live in `web/index.html`.

## Build, Test, and Development Commands

- `go build ./...` compiles every package with the Go 1.26 toolchain.
- `go test ./...` runs the complete unit and integration-style test suite.
- `go test -race ./...` checks concurrent paths for data races.
- `go vet ./...` performs standard static analysis.
- `go run . --help` lists CLI commands; `go run . serve` starts the service when the required secret files and paths are configured.
- `docker compose up -d --build` builds and runs the production-like scratch image locally.

Run `gofmt -w <files>` and the test commands before submitting changes.

## Coding Style & Naming Conventions

Follow idiomatic Go and let `gofmt` define tabs and layout. Use short, lower-case package names and descriptive exported identifiers; document exported APIs when their purpose is not self-evident. Keep packages focused, return errors with operation context using `%w`, and pass `context.Context` through blocking or network operations. Prefer concrete implementations and small consumer-owned interfaces over speculative abstractions. Add configuration through the existing Cobra/Viper flow and use the `BILI_NOTIFY_*` environment naming pattern.

## Testing Guidelines

Use the standard `testing` package, table-driven cases where inputs vary, `httptest` for HTTP boundaries, and `t.TempDir()` for stateful tests. Name tests `TestBehavior` and mark reusable helpers with `t.Helper()`. There is no numeric coverage threshold; every behavioral change should include focused success and failure cases, followed by `go test -race ./...`.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit subjects such as `feat: ...` and `fix: ...`; continue that pattern with an imperative, scoped summary. Pull requests should explain the user-visible behavior, operational or security impact, tests run, and any configuration changes. Link relevant issues and include screenshots for administration UI changes. Never commit generated secrets, Bilibili cookies, OAuth tokens, webhook URLs, databases, or TLS private keys; keep local secret material under the ignored `secrets/` path.

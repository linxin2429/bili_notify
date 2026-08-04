# Repository Guidelines

## Project Structure & Module Organization

`main.go` starts the Cobra CLI; command definitions live in `cmd/`, and `app/` wires the service together. Domain packages are organized by responsibility: `bilibili/` calls and parses Bilibili APIs, `service/` runs polling and delivery workflows, `notify/` implements notification channels, `state/` persists data in bbolt, `vault/` encrypts secrets, `web/` serves the TLS administration UI, and `model/` holds shared types. Startup configuration belongs in `config/` (paths, listen addresses, log level; collector knobs are seed defaults only and then live in bbolt). Keep architectural decisions synchronized with `docs/requirements-and-design.md`. Tests sit beside their packages as `*_test.go`; browser assets currently live in `web/index.html`.

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

Use the standard `testing` package and `github.com/stretchr/testify`. Name tests `TestBehavior` and mark reusable helpers with `t.Helper()`. Prefer `httptest` for HTTP boundaries and `t.TempDir()` for stateful tests. There is no numeric coverage threshold; every behavioral change should include focused success and failure cases, followed by `go test -race ./...`.

### Table-driven tests

All multi-case scenarios must use table-driven form:

```go
tests := []struct {
    name string
    // inputs, expected outputs, expected errors
}{
    // ...
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        t.Parallel()
        // ...
    })
}
```

Always isolate cases with `t.Run(tt.name, ...)`. Call `t.Parallel()` inside subtests so cases can run concurrently.

### Testify assertions

Import both `"github.com/stretchr/testify/assert"` and `"github.com/stretchr/testify/require"`.

- **Hard stop (`require`)**: setup, `err != nil` checks, and any precondition later logic depends on. Failures must abort immediately (`require.NoError`, `require.NotNil`, …).
- **Soft assert (`assert`)**: multi-field comparisons and independent checks so one mismatch does not hide the rest (`assert.Equal(t, want, got)`).

### Resource cleanup

For temporary files, mock servers, DB handles, or async goroutine control, **do not use `defer`**. Register cleanup with `t.Cleanup(func() { ... })`.

### Path coverage

- **Happy path**: normal inputs and expected outputs.
- **Edge cases**: empty string, nil, zero values, extremes, overflow-prone sizes.
- **Error paths**: every branch that returns `error`. Prefer `errors.Is(gotErr, expectedErr)` (or equivalent typed matching) over a bare `err != nil`.

### No flaky tests

Never use bare `time.Sleep` to wait for concurrent work. Synchronize with channels, or poll with a timeout via `assert.Eventually` / `require.Eventually`.

### Fuzz tests (optional)

When the code under test is string transform, serialize/deserialize (JSON/YAML/XML), protocol pack/unpack, or a core pure algorithm, also add a native Go `Fuzz*` test.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit subjects such as `feat: ...` and `fix: ...`; continue that pattern with an imperative, scoped summary. Pull requests should explain the user-visible behavior, operational or security impact, tests run, and any configuration changes. Link relevant issues and include screenshots for administration UI changes. Never commit generated secrets, Bilibili cookies, OAuth tokens, webhook URLs, databases, or TLS private keys; keep local secret material under the ignored `secrets/` path.

MODULE := github.com/linxin2429/bili_notify
REQUIRED_GO_TOOLCHAIN := go$(shell awk '/^go / { print $$2; exit }' go.mod)

BINARY ?= bili-notify
GO_PACKAGES ?= ./...
GO_TEST_FLAGS ?=
GO_STABILITY_COUNT ?= 10
DOCKER_IMAGE ?= bili-notify:local
DOCKER_BUILD ?= docker build
DOCKER_BUILD_FLAGS ?=
GOPROXY ?= https://mirrors.aliyun.com/goproxy,direct
VERSION ?= dev
COMMIT ?= none
BUILD_DATE ?= unknown
PLAYWRIGHT_INSTALL_FLAGS ?=
COVERAGE_FILE ?= coverage.out
COVERAGE_MIN ?= 80.0
ACTIONLINT_VERSION ?= v1.7.7
OPENAPI_GENERATED_PATHS ?= api/openapi.yaml web/generated web/ui/src/shared/api/generated
ARGS ?= serve
COMPOSE ?= docker compose
COMPOSE_FLAGS ?=
COMPOSE_RUN_FLAGS ?=

LDFLAGS := -s -w \
	-X $(MODULE)/cmd.version=$(VERSION) \
	-X $(MODULE)/cmd.commit=$(COMMIT) \
	-X $(MODULE)/cmd.date=$(BUILD_DATE)

.PHONY: help setup frontend-install frontend-contract-check frontend-typecheck frontend-build frontend-bundle-check frontend-lint frontend-test frontend-coverage frontend-audit frontend-quality playwright-install frontend-e2e go-check-ready check-diff check-fmt check-mod workflow-lint check-coverage-race check-vet check-vulncheck build clean fmt test test-race test-stability test-protocol benchmark coverage coverage-race vet vulncheck ci-check check run docker-build docker-smoke-image docker-smoke observability-validate observability-smoke compose-pull compose-up compose-stop compose-down compose-logs compose-run compose-exec compose-healthcheck

help:
	@printf '%s\n' \
		'Development:' \
		'  setup                 enable repository Git hooks' \
		'  build                 build web/dist and the local bili-notify binary' \
		'  clean                 remove local build and test artifacts' \
		'  run ARGS=serve        run the CLI; override ARGS for another command' \
		'  fmt                    format all Go packages' \
		'  test                   run Go tests' \
		'  test-race              run shuffled Go tests with the race detector' \
		'  test-stability         repeat shuffled race tests (default: 10 times)' \
		'  test-protocol          repeat notification and telemetry protocol tests 3 times under race' \
		'  benchmark              run deterministic protocol and delivery benchmarks' \
		'  coverage               run the core Go coverage gate' \
		'  coverage-race          run the race detector and core Go coverage gate together' \
		'  vet                    run go vet' \
		'  vulncheck              run govulncheck' \
		'  ci-check               run CI checks; the image job performs the production build' \
		'  check                  run the complete local CI check suite' \
		'' \
		'Frontend:' \
		'  frontend-build         install locked dependencies and build the UI' \
		'  frontend-contract-check regenerate OpenAPI client and reject any diff' \
		'  frontend-typecheck     run the TypeScript compiler check' \
		'  frontend-lint          run ESLint, Hooks/Compiler and architecture checks' \
		'  frontend-test          run frontend unit tests' \
		'  frontend-coverage      run the frontend coverage gate' \
		'  frontend-audit         fail on high-severity npm vulnerabilities' \
		'  frontend-bundle-check  build and enforce gzip bundle budgets' \
		'  frontend-quality       run the ordered frontend quality gate' \
		'  playwright-install     install Chromium' \
		'  frontend-e2e           build the UI once and run Playwright tests' \
		'' \
		'Docker:' \
		'  docker-build           build DOCKER_IMAGE (default: bili-notify:local)' \
		'  docker-smoke-image     smoke-test an existing DOCKER_IMAGE' \
		'  docker-smoke           build and smoke-test DOCKER_IMAGE' \
		'  observability-validate validate Compose, Collector, Prometheus, Loki, and Tempo configs' \
		'  observability-smoke    query a running full observability stack' \
		'  compose-pull           pull Compose images' \
		'  compose-up             start Compose services' \
		'  compose-stop           stop the bili-notify service' \
		'  compose-down           remove Compose services' \
		'  compose-logs           follow bili-notify logs' \
		'  compose-run ARGS=...   run a one-off CLI command' \
		'  compose-exec ARGS=...  run a command in the service container' \
		'  compose-healthcheck    check the running service'

setup:
	git config core.hooksPath .githooks

frontend-install:
	npm --prefix web/ui ci

frontend-contract-check: frontend-install
	OPENAPI_GENERATED_PATHS="$(OPENAPI_GENERATED_PATHS)" npm --prefix web/ui run api:check

frontend-typecheck: frontend-install
	npm --prefix web/ui run typecheck

frontend-build: frontend-install
	npm --prefix web/ui run build

frontend-bundle-check: frontend-build
	npm --prefix web/ui run bundle:check

frontend-lint: frontend-install
	npm --prefix web/ui run lint

frontend-test: frontend-install
	npm --prefix web/ui test

frontend-coverage: frontend-install
	npm --prefix web/ui run test:coverage

frontend-audit: frontend-install
	npm --prefix web/ui audit --audit-level=high

# Keep generation, static analysis, tests, build and the manifest budget in this
# order so no check observes stale generated code or stale production assets.
frontend-quality: frontend-install
	npm --prefix web/ui audit --audit-level=high
	OPENAPI_GENERATED_PATHS="$(OPENAPI_GENERATED_PATHS)" npm --prefix web/ui run api:check
	npm --prefix web/ui run typecheck
	npm --prefix web/ui run lint
	npm --prefix web/ui run test:coverage
	npm --prefix web/ui run build
	npm --prefix web/ui run bundle:check

playwright-install: frontend-install
	cd web/ui && npx playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium

frontend-e2e: frontend-quality playwright-install
	npm --prefix web/ui run test:e2e:run

# Playwright and Vitest replace transient directories below web/ui. Finish the
# ordered frontend gate before `go ... ./...` walks the repository.
go-check-ready: frontend-e2e

check-diff:
	git diff --check

check-fmt:
	@files="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$${files}" ]; then \
		echo 'The following Go files are not formatted:'; \
		printf '%s\n' "$${files}"; \
		exit 1; \
	fi

check-mod:
	@diff="$$(go mod tidy -diff)"; \
	if [ -n "$${diff}" ]; then \
		printf '%s\n' "$${diff}"; \
		exit 1; \
	fi
	go mod verify

workflow-lint:
	GOTOOLCHAIN=$(REQUIRED_GO_TOOLCHAIN) go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION) .github/workflows/*.yml

check-vet: go-check-ready
	go vet $(GO_PACKAGES)

check-vulncheck: go-check-ready
	GOTOOLCHAIN=$(REQUIRED_GO_TOOLCHAIN) go tool govulncheck $(GO_PACKAGES)

build: frontend-build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINARY)" .

clean:
	rm -f -- "$(BINARY)" "$(COVERAGE_FILE)" web/ui/*.tsbuildinfo
	rm -rf -- web/dist web/ui/node_modules web/ui/coverage web/ui/test-results web/ui/playwright-report

fmt:
	go fmt $(GO_PACKAGES)

test: frontend-build
	go test $(GO_TEST_FLAGS) $(GO_PACKAGES)

test-race: frontend-build
	go test -race -shuffle=on $(GO_TEST_FLAGS) $(GO_PACKAGES)

test-stability: frontend-build
	@count="$${GO_STABILITY_COUNT:-10}"; \
	case "$${count}" in *[!0-9]*|'') echo 'GO_STABILITY_COUNT must be an integer from 1 to 50' >&2; exit 2;; esac; \
	if [ "$${count}" -lt 1 ] || [ "$${count}" -gt 50 ]; then \
		echo 'GO_STABILITY_COUNT must be an integer from 1 to 50' >&2; \
		exit 2; \
	fi; \
	iteration=1; \
	while [ "$${iteration}" -le "$${count}" ]; do \
		seed="$$(od -An -N4 -tu4 /dev/urandom | tr -d ' ')"; \
		echo "Stability iteration $${iteration}/$${count}, shuffle seed $${seed}"; \
		go test -race -shuffle="$${seed}" -count=1 $(GO_TEST_FLAGS) $(GO_PACKAGES) || exit $$?; \
		iteration=$$((iteration + 1)); \
	done

test-protocol: frontend-build
	go test -race -shuffle=on -count=3 $(GO_TEST_FLAGS) ./notify ./telemetry

benchmark: frontend-build
	go test -run='^$$' -bench=. -benchmem ./notify ./service ./telemetry

coverage: frontend-build
	@set -eu; \
	core_packages="$$(go list ./bilibili ./notify ./service ./state ./web | paste -sd, -)"; \
	go test $(GO_TEST_FLAGS) -covermode=atomic -coverpkg="$${core_packages}" -coverprofile="$(COVERAGE_FILE)" ./...; \
	total="$$(go tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}')"; \
	echo "Core Go coverage: $${total}%"; \
	awk -v coverage="$${total}" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) exit 1 }'

coverage-race: frontend-build
	@set -eu; \
	core_packages="$$(go list ./bilibili ./notify ./service ./state ./web | paste -sd, -)"; \
	go test -race -shuffle=on $(GO_TEST_FLAGS) -covermode=atomic -coverpkg="$${core_packages}" -coverprofile="$(COVERAGE_FILE)" ./...; \
	total="$$(go tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}')"; \
	echo "Core Go coverage: $${total}%"; \
	awk -v coverage="$${total}" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) exit 1 }'

check-coverage-race: go-check-ready
	@set -eu; \
	core_packages="$$(go list ./bilibili ./notify ./service ./state ./web | paste -sd, -)"; \
	go test -race -shuffle=on $(GO_TEST_FLAGS) -covermode=atomic -coverpkg="$${core_packages}" -coverprofile="$(COVERAGE_FILE)" ./...; \
	total="$$(go tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}')"; \
	echo "Core Go coverage: $${total}%"; \
	awk -v coverage="$${total}" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) exit 1 }'

vet: frontend-build
	go vet $(GO_PACKAGES)

vulncheck: frontend-build
	GOTOOLCHAIN=$(REQUIRED_GO_TOOLCHAIN) go tool govulncheck $(GO_PACKAGES)

ci-check: check-diff check-fmt check-mod workflow-lint go-check-ready check-coverage-race check-vet check-vulncheck

check: build ci-check

run: frontend-build
	go run . $(ARGS)

docker-build:
	$(DOCKER_BUILD) $(DOCKER_BUILD_FLAGS) \
		--build-arg "GOPROXY=$(GOPROXY)" \
		--build-arg "VERSION=$(VERSION)" \
		--build-arg "COMMIT=$(COMMIT)" \
		--build-arg "BUILD_DATE=$(BUILD_DATE)" \
		--tag "$(DOCKER_IMAGE)" .

docker-smoke-image:
	./e2e/docker-smoke.sh "$(DOCKER_IMAGE)"

docker-smoke: docker-build
	$(MAKE) docker-smoke-image DOCKER_IMAGE="$(DOCKER_IMAGE)"

observability-validate:
	GRAFANA_ADMIN_PASSWORD=validation $(COMPOSE) -f compose.yaml -f compose.observability.yaml --profile observability config >/dev/null
	GRAFANA_ADMIN_PASSWORD=validation $(COMPOSE) -f compose.full.yaml config >/dev/null
	docker run --rm -v "$(CURDIR)/deploy/observability/otel-collector.yaml:/etc/otelcol/config.yaml:ro" otel/opentelemetry-collector-contrib:0.158.0 validate --config=/etc/otelcol/config.yaml
	docker run --rm --entrypoint=/bin/promtool -v "$(CURDIR)/deploy/observability:/etc/prometheus:ro" prom/prometheus:v3.13.2 check config /etc/prometheus/prometheus.yaml
	docker run --rm --entrypoint=/bin/promtool -w /etc/prometheus/rules -v "$(CURDIR)/deploy/observability:/etc/prometheus:ro" prom/prometheus:v3.13.2 test rules bili-notify.test.yaml
	docker run --rm -v "$(CURDIR)/deploy/observability/loki.yaml:/etc/loki/loki.yaml:ro" grafana/loki:3.7.6 -config.file=/etc/loki/loki.yaml -verify-config
	docker run --rm -v "$(CURDIR)/deploy/observability/tempo.yaml:/etc/tempo/tempo.yaml:ro" grafana/tempo:3.0.2 -config.file=/etc/tempo/tempo.yaml -config.verify=true

observability-smoke:
	./e2e/observability-smoke.sh compose.full.yaml

compose-pull:
	$(COMPOSE) $(COMPOSE_FLAGS) pull

compose-up:
	$(COMPOSE) $(COMPOSE_FLAGS) up -d

compose-stop:
	$(COMPOSE) $(COMPOSE_FLAGS) stop bili-notify

compose-down:
	$(COMPOSE) $(COMPOSE_FLAGS) down

compose-logs:
	$(COMPOSE) $(COMPOSE_FLAGS) logs -f bili-notify

compose-run:
	$(COMPOSE) $(COMPOSE_FLAGS) run --rm $(COMPOSE_RUN_FLAGS) bili-notify $(ARGS)

compose-exec:
	$(COMPOSE) $(COMPOSE_FLAGS) exec bili-notify $(ARGS)

compose-healthcheck:
	$(COMPOSE) $(COMPOSE_FLAGS) exec bili-notify /bili-notify healthcheck

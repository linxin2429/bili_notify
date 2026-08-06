MODULE := github.com/linxin2429/bili_notify

BINARY ?= bili-notify
GO_PACKAGES ?= ./...
GO_TEST_FLAGS ?=
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
ARGS ?= serve
COMPOSE ?= docker compose
COMPOSE_FLAGS ?=
COMPOSE_RUN_FLAGS ?=

LDFLAGS := -s -w \
	-X $(MODULE)/cmd.version=$(VERSION) \
	-X $(MODULE)/cmd.commit=$(COMMIT) \
	-X $(MODULE)/cmd.date=$(BUILD_DATE)

.NOTPARALLEL: check

.PHONY: help setup frontend-install frontend-build frontend-lint frontend-test frontend-coverage playwright-install frontend-e2e build clean fmt test test-race coverage vet vulncheck check run docker-build docker-smoke compose-pull compose-up compose-stop compose-down compose-logs compose-run compose-exec compose-healthcheck

help:
	@printf '%s\n' \
		'Development:' \
		'  setup                 enable repository Git hooks' \
		'  build                 build web/dist and the local bili-notify binary' \
		'  clean                 remove local build and test artifacts' \
		'  run ARGS=serve        run the CLI; override ARGS for another command' \
		'  fmt                    format all Go packages' \
		'  test                   run Go tests' \
		'  test-race              run Go tests with the race detector' \
		'  coverage               run the core Go coverage gate' \
		'  vet                    run go vet' \
		'  vulncheck              run govulncheck' \
		'  check                  run the complete local CI check suite' \
		'' \
		'Frontend:' \
		'  frontend-build         install locked dependencies and build the UI' \
		'  frontend-lint          run the TypeScript check' \
		'  frontend-test          run frontend unit tests' \
		'  frontend-coverage      run the frontend coverage gate' \
		'  playwright-install     install Chromium' \
		'  frontend-e2e           build the UI and run Playwright tests' \
		'' \
		'Docker:' \
		'  docker-build           build DOCKER_IMAGE (default: bili-notify:local)' \
		'  docker-smoke           build and smoke-test DOCKER_IMAGE' \
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

frontend-build: frontend-install
	npm --prefix web/ui run build

frontend-lint: frontend-install
	npm --prefix web/ui run lint

frontend-test: frontend-install
	npm --prefix web/ui test

frontend-coverage: frontend-install
	npm --prefix web/ui run test:coverage

playwright-install: frontend-install
	cd web/ui && npx playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium

frontend-e2e: playwright-install
	npm --prefix web/ui run test:e2e

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
	go test -race $(GO_TEST_FLAGS) $(GO_PACKAGES)

coverage: frontend-build
	@set -eu; \
	core_packages="$$(go list ./bilibili ./notify ./service ./state ./web | paste -sd, -)"; \
	go test -covermode=atomic -coverpkg="$${core_packages}" -coverprofile="$(COVERAGE_FILE)" ./...; \
	total="$$(go tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}')"; \
	echo "Core Go coverage: $${total}%"; \
	awk -v coverage="$${total}" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) exit 1 }'

vet: frontend-build
	go vet $(GO_PACKAGES)

vulncheck: frontend-build
	go run golang.org/x/vuln/cmd/govulncheck@latest $(GO_PACKAGES)

check: build frontend-lint frontend-coverage frontend-e2e coverage test-race vet vulncheck

run: frontend-build
	go run . $(ARGS)

docker-build:
	$(DOCKER_BUILD) $(DOCKER_BUILD_FLAGS) \
		--build-arg "GOPROXY=$(GOPROXY)" \
		--build-arg "VERSION=$(VERSION)" \
		--build-arg "COMMIT=$(COMMIT)" \
		--build-arg "BUILD_DATE=$(BUILD_DATE)" \
		--tag "$(DOCKER_IMAGE)" .

docker-smoke: docker-build
	./e2e/docker-smoke.sh "$(DOCKER_IMAGE)"

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

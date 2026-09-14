# `make check` reproduces the CI gates locally (design v3 §1.1).
# If this passes and CI fails, that is a bug in this file.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO           ?= go
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE         ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
MODULE       := github.com/akynte/local-engineer
LDFLAGS      := -s -w \
                -X $(MODULE)/internal/version.Version=$(VERSION) \
                -X $(MODULE)/internal/version.Commit=$(COMMIT) \
                -X $(MODULE)/internal/version.Date=$(DATE)
BIN          := bin
IMAGE        ?= local-engineer:dev
DOCKER_TARGET?= cpu

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the le binary into ./bin
	@mkdir -p $(BIN)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)/le ./cmd/le
	@echo "built $(BIN)/le ($(VERSION))"

.PHONY: install
install: ## Install le into $$GOBIN
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags="$(LDFLAGS)" ./cmd/le

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: check
check: fmt-check vet storescope lint staticcheck test isolation schemas taskset ## Everything CI runs

.PHONY: fmt-check
fmt-check: ## Fail if anything is not gofmt'd
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
	@echo "gofmt: clean"

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: storescope
storescope: ## Enforce the §2.3 isolation rule
	@$(GO) run ./tools/analyzers/storescope/cmd/storescope ./... && echo "storescope: clean"

.PHONY: lint
lint: ## golangci-lint (skipped with a warning if not installed)
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  golangci-lint run --timeout=6m; \
	else \
	  echo "golangci-lint not installed; CI will still run it."; \
	  echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2"; \
	fi

.PHONY: staticcheck
staticcheck: ## staticcheck (skipped with a warning if not installed)
	@if command -v staticcheck >/dev/null 2>&1; then \
	  staticcheck ./...; \
	else \
	  echo "staticcheck not installed; CI will still run it."; \
	  echo "  go install honnef.co/go/tools/cmd/staticcheck@v0.8.1"; \
	fi

.PHONY: test
test: ## Unit tests with the race detector
	$(GO) test -race -timeout 10m ./...

.PHONY: isolation
isolation: ## The workspace isolation tests, and proof they can fail
	$(GO) test -race -v -run 'Isolation|CrossWorkspace|Workspace|Guard|Cache' ./internal/store/...
	@echo "--- confirming the isolation tests can actually fail ---"
	@./scripts/verify-isolation-can-fail.sh

.PHONY: schemas
schemas: ## Migrations apply, profiles validate, compose files parse
	$(GO) test -run 'TestMigrations|TestShippedProfiles' ./internal/... 
	docker compose -f deploy/docker-compose.yml config -q
	LE_MODEL=placeholder.gguf docker compose -f deploy/docker-compose.split.yml config -q
	$(MAKE) --no-print-directory benchmarks-check
	@echo "schemas: valid"

.PHONY: sidecar-test
sidecar-test: ## Run the Node sidecar's own tests
	@if command -v npm >/dev/null 2>&1; then \
	  cd sidecars/typescript && npm ci --no-audit --no-fund >/dev/null 2>&1 || npm install --no-audit --no-fund >/dev/null && npm test; \
	else \
	  echo "sidecar-test: npm not installed, skipping"; \
	fi

.PHONY: benchmarks
benchmarks: ## Regenerate BENCHMARKS.md from the committed results
	scripts/make-benchmarks.sh > BENCHMARKS.md
	@echo "wrote BENCHMARKS.md"

.PHONY: benchmarks-check
benchmarks-check: ## Fail if BENCHMARKS.md is out of date with the results
	@scripts/make-benchmarks.sh > /tmp/benchmarks.check.$$$$ && \
	  if ! diff -q BENCHMARKS.md /tmp/benchmarks.check.$$$$ >/dev/null; then \
	    echo "BENCHMARKS.md is out of date with docs/benchmarks/results/." >&2; \
	    echo "Run 'make benchmarks' and commit the result." >&2; \
	    diff -u BENCHMARKS.md /tmp/benchmarks.check.$$$$ | head -40 >&2; \
	    rm -f /tmp/benchmarks.check.$$$$; exit 1; \
	  fi; rm -f /tmp/benchmarks.check.$$$$
	@echo "BENCHMARKS.md: current"

.PHONY: split-smoke
split-smoke: ## Exercise the split layout against a stub inference service
	docker build -f deploy/Dockerfile --target cpu -t local-engineer:split-smoke .
	LE_IMAGE=local-engineer:split-smoke scripts/check-split-layout.sh

.PHONY: docs-test
docs-test: build ## Execute the command blocks in the how-to pages
	PATH="$(CURDIR)/$(BIN):$$PATH" $(GO) test -tags=docs -run TestDocumentedCommands ./docs/...

.PHONY: taskset
taskset: ## Validate the evaluation task set
	$(GO) test -run 'TestShippedTaskSet|TestObjectives|TestFixtures|TestAcceptanceFails|TestKnownGood' ./internal/eval/

.PHONY: bench
bench: ## Run the storage and graph benchmarks
	$(GO) test -run '^$$' -bench . -benchmem -count 6 ./evals/...

.PHONY: cover
cover: ## Coverage report (a diagnostic, never a gate)
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1
	@echo "html: go tool cover -html=coverage.out"

.PHONY: vuln
vuln: ## govulncheck
	@command -v govulncheck >/dev/null 2>&1 || $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

.PHONY: image
image: ## Build the container image (DOCKER_TARGET=slim|cpu|cuda)
	docker build -f deploy/Dockerfile --target $(DOCKER_TARGET) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
	  -t $(IMAGE) .

.PHONY: image-smoke
image-smoke: image ## Build the image, start it, and require /readyz
	@set -e; \
	docker rm -f le-smoke >/dev/null 2>&1 || true; \
	docker volume rm le-smoke-data >/dev/null 2>&1 || true; \
	docker volume create le-smoke-data >/dev/null; \
	docker run --rm -v le-smoke-data:/data alpine chown -R 10001:10001 /data; \
	docker run -d --name le-smoke -v le-smoke-data:/data -p 127.0.0.1:7777:7777 $(IMAGE) >/dev/null; \
	for i in $$(seq 1 60); do \
	  if curl -fsS http://127.0.0.1:7777/readyz >/dev/null 2>&1; then \
	    echo "ready after $${i}s"; curl -fsS http://127.0.0.1:7777/readyz; \
	    docker rm -f le-smoke >/dev/null; docker volume rm le-smoke-data >/dev/null; exit 0; \
	  fi; sleep 1; \
	done; \
	echo "never became ready:"; docker logs le-smoke; \
	docker rm -f le-smoke >/dev/null; exit 1

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN) dist coverage.out

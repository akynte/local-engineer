# Dev container. Pins the same toolchain CI uses, so `make check` locally and
# CI agree.
FROM mcr.microsoft.com/devcontainers/go:1-1.26-bookworm

# The tools the CI gates run. Versions match .github/workflows/ci.yml; when one
# moves there, move it here too or `make check` stops matching CI.
ARG GOLANGCI_VERSION=v1.62.2
ARG STATICCHECK_VERSION=2024.1.1

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive \
    && apt-get install -y --no-install-recommends \
       ripgrep jq bubblewrap sqlite3 \
    && rm -rf /var/lib/apt/lists/*

USER vscode

RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@${GOLANGCI_VERSION} \
 && go install honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION} \
 && go install golang.org/x/vuln/cmd/govulncheck@latest \
 && go install golang.org/x/perf/cmd/benchstat@latest \
 && go install golang.org/x/tools/gopls@latest \
 && go clean -cache -modcache -testcache 2>/dev/null || true

ENV LE_DATA=/tmp/le-dev-data

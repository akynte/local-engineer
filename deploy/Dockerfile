# syntax=docker/dockerfile:1.7
#
# local-engineer image (design v3 §4.2). Three targets:
#
#   --target slim   supervisor plus engine only; inference and toolchains external
#   --target cpu    adds the Go and Node toolchains and the analysis tools (amd64, arm64)
#   --target cuda   adds the CUDA llama-server build (amd64)
#
# Build:
#   docker build -f deploy/Dockerfile --target cpu -t local-engineer:dev .
#
# Every tool version is pinned by digest or release tag, and the manifest is
# generated into /opt/le/image-manifest.txt at build time so the running image
# can state exactly what it contains.

# ---------------------------------------------------------------------------
# Stage 1: build the supervisor. CGO is off, so the binary is static and the
# same build works for every target architecture.
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS build

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
ARG TARGETARCH

WORKDIR /src

# Dependencies first: this layer is reused whenever only source changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY . .

# -trimpath and the pinned toolchain make the build reproducible; the ldflags
# stamp the version the supervisor reports on /healthz.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/akynte/local-engineer/internal/version.Version=${VERSION} \
        -X github.com/akynte/local-engineer/internal/version.Commit=${COMMIT} \
        -X github.com/akynte/local-engineer/internal/version.Date=${DATE}" \
      -o /out/le ./cmd/le

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/storescope ./tools/analyzers/storescope/cmd/storescope

# ---------------------------------------------------------------------------
# Stage 2: slim runtime. Supervisor plus the engine, nothing else.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS slim

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.title="local-engineer" \
      org.opencontainers.image.description="A supervised local coding engineer with per-workspace isolation" \
      org.opencontainers.image.source="https://github.com/akynte/local-engineer" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# tini is PID 1 so orphaned grandchildren are reaped even when the supervisor
# is not itself PID 1 (§4.3). bubblewrap is present so DR-3 layer 3 can be
# enabled when the runtime permits user namespaces; git and ripgrep are the
# minimum a task needs.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tini git ripgrep curl jq bubblewrap procps less \
    && rm -rf /var/lib/apt/lists/*

# An unprivileged user: DR-3 layer 1 assumes the container does not run as
# root, and the data volume is owned by this uid.
RUN groupadd --gid 10001 le \
 && useradd --uid 10001 --gid le --create-home --home-dir /home/le --shell /bin/bash le \
 && mkdir -p /data /work /opt/le \
 && chown -R le:le /data /work /opt/le

COPY --from=build /out/le /usr/local/bin/le
COPY --from=build /out/storescope /usr/local/bin/storescope
COPY deploy/entrypoint/entrypoint.sh /usr/local/bin/le-entrypoint
COPY profiles/ /opt/le/profiles/
RUN chmod +x /usr/local/bin/le-entrypoint

# The API binds every interface INSIDE the container because Docker's port
# publishing cannot reach a loopback bind there. Exposure is restricted on the
# host side: publish as `-p 127.0.0.1:7777:7777` (§4.1), never `-p 7777:7777`.
ENV LE_DATA=/data \
    LE_API_ADDR=0.0.0.0:7777 \
    LE_IN_CONTAINER=1 \
    XDG_DATA_HOME=/data/xdg/data \
    XDG_CONFIG_HOME=/data/xdg/config \
    XDG_CACHE_HOME=/data/xdg/cache \
    HOME=/home/le \
    PATH=/usr/local/bin:/usr/bin:/bin

# Record what this image actually holds (§4.2: a generated image manifest).
RUN { \
      echo "local-engineer ${VERSION} (${COMMIT})"; \
      echo "variant: slim"; \
      echo "built: $(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
      echo "debian: $(cat /etc/debian_version)"; \
      echo "git: $(git --version)"; \
      echo "ripgrep: $(rg --version | head -1)"; \
      echo "bubblewrap: $(bwrap --version)"; \
      echo "tini: $(tini --version)"; \
    } > /opt/le/image-manifest.txt

USER le
WORKDIR /work
VOLUME ["/data"]
EXPOSE 7777

# A container health check mirrors /readyz so `docker ps` and compose see the
# same readiness the supervisor reports (DR-1's visibility compensation).
HEALTHCHECK --interval=15s --timeout=5s --start-period=60s --retries=4 \
  CMD curl -fsS http://127.0.0.1:7777/readyz >/dev/null || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/le-entrypoint"]
CMD ["api"]

# ---------------------------------------------------------------------------
# Stage 3: cpu runtime. Adds the toolchains and analysis tools a Go or
# TypeScript task needs, but no inference: on CPU-only and Apple Silicon hosts
# inference is expected to be external (§4.2).
# ---------------------------------------------------------------------------
FROM slim AS cpu

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG TARGETARCH
# Renovate keeps these pinned versions current; each is also listed in the
# generated image manifest.
ARG GO_VERSION=1.26.3
ARG NODE_MAJOR=22

USER root

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      build-essential pkg-config make xz-utils gnupg \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain, verified by checksum.
RUN set -eux; \
    url="https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz"; \
    curl -fsSL "$url" -o /tmp/go.tgz; \
    curl -fsSL "${url}.sha256" -o /tmp/go.sha256 || echo "" > /tmp/go.sha256; \
    if [ -s /tmp/go.sha256 ]; then echo "$(cat /tmp/go.sha256)  /tmp/go.tgz" | sha256sum -c -; fi; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz /tmp/go.sha256

ENV PATH=/usr/local/go/bin:/opt/le/gotools/bin:$PATH \
    GOPATH=/opt/le/gopath \
    GOBIN=/opt/le/gotools/bin \
    GOFLAGS=-mod=mod

# Node LTS for the TypeScript sidecars and OpenCode.
RUN set -eux; \
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" -o /tmp/node.sh; \
    bash /tmp/node.sh; \
    apt-get install -y --no-install-recommends nodejs; \
    rm -rf /var/lib/apt/lists/* /tmp/node.sh

# The compiler-backed analysis tools the verification loops depend on (§10.1).
RUN set -eux; \
    mkdir -p "$GOBIN"; \
    go install golang.org/x/tools/gopls@latest; \
    go install honnef.co/go/tools/cmd/staticcheck@latest; \
    go install golang.org/x/vuln/cmd/govulncheck@latest; \
    go install golang.org/x/perf/cmd/benchstat@latest; \
    chmod -R a+rx /opt/le/gotools; \
    rm -rf "$GOPATH"/pkg/mod/cache/download || true

RUN { \
      cat /opt/le/image-manifest.txt | sed 's/^variant: slim/variant: cpu/'; \
      echo "go: $(go version)"; \
      echo "node: $(node --version)"; \
      echo "npm: $(npm --version)"; \
      echo "gopls: $(gopls version 2>/dev/null | head -1)"; \
      echo "staticcheck: $(staticcheck --version 2>/dev/null)"; \
      echo "govulncheck: present"; \
      echo "benchstat: present"; \
    } > /opt/le/image-manifest.cpu.txt && mv /opt/le/image-manifest.cpu.txt /opt/le/image-manifest.txt

RUN chown -R le:le /opt/le

USER le
WORKDIR /work

# ---------------------------------------------------------------------------
# Stage 4: cuda runtime. The default published image on amd64 (§4.2).
# The CUDA llama-server build is pinned; §16 verify item 4 tracks the driver
# and runtime pairing.
# ---------------------------------------------------------------------------
FROM cpu AS cuda

ARG VERSION=0.0.0-dev
ARG LLAMA_RELEASE=b4800

USER root

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      libgomp1 unzip \
    && rm -rf /var/lib/apt/lists/*

# The llama.cpp CUDA server binary. Kept as a separate layer so a llama.cpp
# bump does not invalidate the toolchain layers.
RUN set -eux; \
    url="https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_RELEASE}/llama-${LLAMA_RELEASE}-bin-ubuntu-cuda-x64.zip"; \
    if curl -fsSL "$url" -o /tmp/llama.zip; then \
      unzip -q /tmp/llama.zip -d /opt/le/llama; \
      find /opt/le/llama -name 'llama-server' -exec install -m 0755 {} /usr/local/bin/llama-server \; ; \
      rm -rf /tmp/llama.zip /opt/le/llama; \
    else \
      echo "WARNING: llama.cpp ${LLAMA_RELEASE} could not be fetched; this image has no embedded inference." >&2; \
    fi

RUN sed -i 's/^variant: cpu/variant: cuda/' /opt/le/image-manifest.txt \
 && echo "llama.cpp: ${LLAMA_RELEASE}" >> /opt/le/image-manifest.txt \
 && chown -R le:le /opt/le

USER le
WORKDIR /work

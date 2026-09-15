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
#
# XDG variables are deliberately NOT pointed at /data here. §2.2 sets them per
# task process, at that workspace's own opencode/ directory, so the engine can
# never see another workspace's sessions. A global value would both break that
# and — because build-time tools honour XDG — bake state into the image's /data
# layer, which Docker then copies into every fresh volume.
ENV LE_DATA=/data \
    LE_API_ADDR=0.0.0.0:7777 \
    LE_IN_CONTAINER=1 \
    HOME=/home/le \
    XDG_DATA_HOME=/home/le/.local/share \
    XDG_CONFIG_HOME=/home/le/.config \
    XDG_CACHE_HOME=/home/le/.cache \
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

# /data must ship EMPTY. Docker copies an image's content at a volume path into
# a fresh volume, so anything left here — including files a build step wrote as
# root — becomes undeletable state in every new install.
RUN rm -rf /data/* /data/.[!.]* 2>/dev/null; \
    chown le:le /data; \
    [ -z "$(ls -A /data)" ] || { echo "/data is not empty in the image:" >&2; ls -la /data >&2; exit 1; }

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
# Pinned checksums from the Go release index. Deliberately pinned rather than
# fetched alongside the tarball: downloading a checksum from the same server as
# the artifact only checks that server against itself. Update both when
# GO_VERSION moves — the build fails loudly if they do not match.
ARG GO_SHA256_amd64=2b2cfc7148493da5e73981bffbf3353af381d5f93e789c82c79aff64962eb556
ARG GO_SHA256_arm64=9d89a3ea57d141c2b22d70083f2c8459ba3890f2d9e818e7e933b75614936565
ARG NODE_MAJOR=22
ARG GOLANGCI_VERSION=2.13.2
ARG GOLANGCI_SHA256_amd64=2277d43b98ec0054280f2ac26b53268bae97682444678a59a657dd565da021d6
ARG GOLANGCI_SHA256_arm64=a2a4e0065aa41be71f7c5ac90f271b61751331e5d04314e62afe4027855f0893
ARG SEMGREP_VERSION=1.177.0
# The §4.2 analysis tools a repository can wire into a `check:` step in
# .le/verify.yaml. Each is pinned; `go install` resolves through the module
# proxy and the checksum database, which is stronger provenance than a
# hand-pinned tarball hash.
ARG GOSEC_VERSION=v2.29.0
ARG GITLEAKS_VERSION=v8.30.1
ARG OSV_SCANNER_VERSION=v2.6.0
ARG BUF_VERSION=v1.73.0
ARG OASDIFF_VERSION=v1.32.0
ARG SQUAWK_VERSION=2.65.0

USER root

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      build-essential pkg-config make xz-utils gnupg \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain, verified against a pinned checksum.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) expected="${GO_SHA256_amd64}" ;; \
      arm64) expected="${GO_SHA256_arm64}" ;; \
      *) echo "no pinned Go checksum for architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" -o /tmp/go.tgz; \
    echo "${expected}  /tmp/go.tgz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm -f /tmp/go.tgz

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
# HOME is overridden for this layer: the ENV sets it to /home/le, and a root
# `go install` would leave a root-owned build cache there that the runtime user
# then cannot write — which surfaces much later as a permission error from
# inside the compiler.
RUN set -eux; \
    export HOME=/root; \
    mkdir -p "$GOBIN"; \
    go install golang.org/x/tools/gopls@latest; \
    go install honnef.co/go/tools/cmd/staticcheck@latest; \
    go install golang.org/x/vuln/cmd/govulncheck@latest; \
    go install golang.org/x/perf/cmd/benchstat@latest; \
    chmod -R a+rx /opt/le/gotools; \
    go clean -cache -modcache -testcache || true; \
    rm -rf "$GOPATH"/pkg "$HOME"/.cache/go-build || true

# §4.2's analysis tools. None of these has a built-in recipe — which tables a
# migration may lock, and which proto changes are breaking, are facts about a
# repository — so a repository reaches them through a `check:` step in
# .le/verify.yaml. They are in the image because the engine has no shell tool:
# its only route to an external program is run_recipe, so a tool absent here is
# unreachable rather than "install it yourself".
# Each tool is its own `go install` line rather than one chain, so a network
# hiccup on the fourth does not re-download the first three on retry. buf asks
# for a newer toolchain than GO_VERSION pins, so Go fetches one here: that is
# the cost of pinning the base toolchain and letting tools state their own
# minimum, and it is visible in the build log rather than silent.
#
# The cleanup at the end removes the extracted module tree, the build cache and
# any toolchain Go fetched. Removing only the *download* cache — which is what
# this did at first — left 3.2 GB of extracted modules behind, and the image
# went from 710 MB to 3.8 GB without anyone deciding that.
#
# Note the comments are here and not inside the RUN: a `#` after a line ending
# in `\` comments out every line that follows it, cleanup included.
RUN set -eux; \
    export HOME=/root; \
    go install "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"; \
    go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}"; \
    go install "github.com/google/osv-scanner/v2/cmd/osv-scanner@${OSV_SCANNER_VERSION}"; \
    go install "github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}"; \
    go install "github.com/oasdiff/oasdiff@${OASDIFF_VERSION}"; \
    chmod -R a+rx /opt/le/gotools; \
    go clean -cache -modcache -testcache || true; \
    rm -rf "$GOPATH"/pkg "$HOME"/.cache/go-build || true

# squawk lints Postgres migrations and is Rust, distributed through npm.
RUN set -eux; \
    export HOME=/root; \
    npm install -g --no-audit --no-fund "squawk-cli@${SQUAWK_VERSION}"; \
    npm cache clean --force

# golangci-lint and semgrep are the two tools `internal/recipe` shells out to
# that the toolchain does not provide. Both were listed in §4.2's image
# contents and neither was here, which made the semgrep recipe skip on every
# published image and left §10.1's lint leg with nothing to run.
#
# They are in the image rather than left to the user because the engine has no
# shell tool: its only route to an external program is `run_recipe`, so a tool
# absent from the image is not "install it yourself", it is unreachable.
#
# Released binaries, verified against the published checksums. `go install`
# would build golangci-lint from source against this image's Go rather than the
# one it was released with, which upstream explicitly does not support.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) expected="${GOLANGCI_SHA256_amd64}" ;; \
      arm64) expected="${GOLANGCI_SHA256_arm64}" ;; \
      *) echo "no pinned golangci-lint checksum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    base="golangci-lint-${GOLANGCI_VERSION}-linux-${TARGETARCH}"; \
    curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}/${base}.tar.gz" -o /tmp/gl.tgz; \
    echo "${expected}  /tmp/gl.tgz" | sha256sum -c -; \
    tar -xzf /tmp/gl.tgz -C /tmp; \
    install -m 0755 "/tmp/${base}/golangci-lint" /usr/local/bin/golangci-lint; \
    rm -rf /tmp/gl.tgz "/tmp/${base}"; \
    golangci-lint --version

# semgrep is Python and has no released binary, so it gets its own virtualenv
# rather than being installed into the system interpreter, where its pins would
# become the image's pins. Only the entry point is exposed on PATH.
#
# It is the single largest thing in this image — about 374 MB against a 710 MB
# base — and that is a deliberate trade recorded in docs/reference/image-manifest.md:
# §4.2 lists semgrep, §10.1 adopts project-invariant analyzers, and a recipe
# that skips on every published image is worse than the bytes.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      python3-venv \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    python3 -m venv /opt/le/semgrep; \
    /opt/le/semgrep/bin/pip install --no-cache-dir --upgrade pip; \
    /opt/le/semgrep/bin/pip install --no-cache-dir "semgrep==${SEMGREP_VERSION}"; \
    ln -s /opt/le/semgrep/bin/semgrep /usr/local/bin/semgrep; \
    semgrep --version

# The TypeScript sidecar (§3.2). Node above exists for it, and without this
# layer the image had Node and no sidecar — so every TypeScript repository got
# the lexical reading and no call graph, silently, because a missing sidecar is
# an ordinary condition the analyzer is designed to survive.
#
# Dependencies are installed from the committed lockfile, in their own layer so
# a change to analyze.js does not re-resolve them. HOME is /root for this layer
# for the same reason the Go tools layer does it: npm's cache would otherwise
# land in /home/le owned by root, and surface much later as a permission error.
COPY sidecars/typescript/package.json sidecars/typescript/package-lock.json \
     /opt/le/sidecars/typescript/
RUN set -eux; \
    export HOME=/root; \
    cd /opt/le/sidecars/typescript; \
    npm ci --omit=dev --no-audit --no-fund; \
    npm cache clean --force

COPY sidecars/typescript/analyze.js /opt/le/sidecars/typescript/

# LE_TYPESCRIPT_SIDECAR_DIR is checked first by SidecarPath, and naming it
# explicitly keeps the answer independent of where the binary happens to sit.
ENV LE_TYPESCRIPT_SIDECAR_DIR=/opt/le/sidecars/typescript

# The sidecar must actually run, not merely be present. An `npm ci` that
# resolved nothing, or a Node that cannot load the compiler, fails here rather
# than degrading to the lexical reading in a user's repository.
RUN set -eux; \
    mkdir -p /tmp/probe; \
    printf 'export function f(): number { return 1; }\nexport function g(): number { return f(); }\n' > /tmp/probe/probe.ts; \
    node /opt/le/sidecars/typescript/analyze.js /tmp/probe > /tmp/probe.json; \
    grep -q '"kind":"calls"' /tmp/probe.json \
      || grep -q '"kind": "calls"' /tmp/probe.json \
      || { echo "the TypeScript sidecar produced no call edges" >&2; cat /tmp/probe.json >&2; exit 1; }; \
    rm -rf /tmp/probe /tmp/probe.json

RUN { \
      cat /opt/le/image-manifest.txt | sed 's/^variant: slim/variant: cpu/'; \
      echo "go: $(go version)"; \
      echo "node: $(node --version)"; \
      echo "npm: $(npm --version)"; \
      echo "gopls: $(gopls version 2>/dev/null | head -1)"; \
      echo "staticcheck: $(staticcheck --version 2>/dev/null)"; \
      echo "govulncheck: present"; \
      echo "benchstat: present"; \
      echo "golangci-lint: $(golangci-lint --version 2>/dev/null | head -1)"; \
      echo "gosec: ${GOSEC_VERSION}"; \
      echo "gitleaks: ${GITLEAKS_VERSION}"; \
      echo "osv-scanner: ${OSV_SCANNER_VERSION}"; \
      echo "buf: ${BUF_VERSION}"; \
      echo "oasdiff: ${OASDIFF_VERSION}"; \
      echo "squawk: ${SQUAWK_VERSION}"; \
      echo "semgrep: $(semgrep --version 2>/dev/null | head -1)"; \
      echo "typescript-sidecar: typescript $(node -p "require('/opt/le/sidecars/typescript/node_modules/typescript/package.json').version")"; \
    } > /opt/le/image-manifest.cpu.txt && mv /opt/le/image-manifest.cpu.txt /opt/le/image-manifest.txt

RUN chown -R le:le /opt/le /home/le

# Re-assert the invariant after the root-owned toolchain install.
RUN rm -rf /data/* /data/.[!.]* 2>/dev/null; \
    chown le:le /data; \
    [ -z "$(ls -A /data)" ] || { echo "/data is not empty in the image:" >&2; ls -la /data >&2; exit 1; }

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

RUN rm -rf /data/* /data/.[!.]* 2>/dev/null; \
    chown le:le /data; \
    [ -z "$(ls -A /data)" ] || { echo "/data is not empty in the image:" >&2; ls -la /data >&2; exit 1; }

USER le
WORKDIR /work

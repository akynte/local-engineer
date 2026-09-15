#!/usr/bin/env bash
# Install local-engineer on the host, without Docker (design v3 §13).
#
# §13: "host-mode installation remains supported as scripts/install-bare-metal.sh
# for developers but is not the documented default."
#
# The two words that matter are *developers* and *not the default*. Docker is
# the supported install (§4.1) because it is what bounds the whole system to
# the repositories you mounted and the data volume you gave it. Running on the
# host means DR-3 layer 1 — the container boundary — is simply absent, and the
# §6.2 guarantees that rest on it do not hold. Landlock still confines each
# task; nothing confines the supervisor.
#
# `le doctor` says this every run when it cannot find a container marker, and
# this script says it once, here, before you commit to anything.
#
#   scripts/install-bare-metal.sh              # build and install to ~/.local/bin
#   scripts/install-bare-metal.sh --prefix /usr/local
#   scripts/install-bare-metal.sh --check      # report readiness, install nothing
set -euo pipefail

PREFIX="${HOME}/.local"
CHECK_ONLY=0
SKIP_SIDECAR=0

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

Options:
  --prefix DIR     install into DIR/bin and DIR/share/local-engineer
                   (default: ~/.local)
  --check          report what is present and what is missing; install nothing
  --no-sidecar     skip the TypeScript sidecar even if Node is available
  -h, --help       this message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) PREFIX="${2:?--prefix needs a directory}"; shift 2 ;;
    --prefix=*) PREFIX="${1#*=}"; shift ;;
    --check) CHECK_ONLY=1; shift ;;
    --no-sidecar) SKIP_SIDECAR=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install-bare-metal: unknown option $1" >&2; usage >&2; exit 2 ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bindir="${PREFIX}/bin"
sharedir="${PREFIX}/share/local-engineer"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  [ok  ] %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*"; }
bad()  { printf '  [FAIL] %s\n' "$*"; }

# ---------------------------------------------------------------------------
# What this install will and will not give you. Said before anything happens,
# because it is the reason the documented default is a container.
# ---------------------------------------------------------------------------
say "local-engineer — host install (design v3 §13)"
say ""
say "This is the developer install, not the documented default."
say ""
say "  What you lose: DR-3 layer 1. The container boundary is what bounds this"
say "  system — tests, builds and every tool a task runs — to the repositories"
say "  you mounted and the data volume you gave it. On the host there is no such"
say "  boundary: a task's verification runs against your real filesystem, with"
say "  only Landlock confining it."
say ""
say "  What you keep: Landlock per task (layer 2), and bubblewrap (layer 3) if"
say "  your kernel permits unprivileged user namespaces. Run 'le doctor' after"
say "  installing; it reports which layers are actually in effect rather than"
say "  which ones the design hopes for."
say ""
say "  The container install is one command and is documented in"
say "  docs/how-to/install.md."
say ""

# ---------------------------------------------------------------------------
# Prerequisites. Reported all at once: finding out about three missing things
# one failed run at a time is the worst way to learn them.
# ---------------------------------------------------------------------------
say "Checking prerequisites:"
missing=0

if command -v go >/dev/null 2>&1; then
  goversion="$(go version | awk '{print $3}')"
  ok "go: ${goversion}"
  # The module targets a recent toolchain; an older one fails to compile in a
  # way that reads as a source error rather than a version mismatch.
  want="$(awk '/^go /{print $2; exit}' "${repo_root}/go.mod")"
  if [ -n "${want}" ]; then
    have="${goversion#go}"
    if [ "$(printf '%s\n%s\n' "${want}" "${have}" | sort -V | head -1)" != "${want}" ]; then
      warn "go.mod asks for ${want}; you have ${have}. The build may fail."
    fi
  fi
else
  bad "go: not found. The supervisor is built from source; install Go first."
  missing=$((missing + 1))
fi

for tool in git; do
  if command -v "$tool" >/dev/null 2>&1; then
    ok "$tool: $("$tool" --version | head -1)"
  else
    bad "$tool: not found. A workspace is a git repository."
    missing=$((missing + 1))
  fi
done

if command -v rg >/dev/null 2>&1; then
  ok "ripgrep: $(rg --version | head -1)"
else
  warn "ripgrep: not found. Lexical search falls back to a slower path."
fi

if command -v node >/dev/null 2>&1; then
  ok "node: $(node --version) — the TypeScript sidecar can be installed"
else
  warn "node: not found. TypeScript will be read lexically, with no call graph."
  SKIP_SIDECAR=1
fi

for tool in golangci-lint semgrep; do
  if command -v "$tool" >/dev/null 2>&1; then
    ok "$tool: present; its verification recipe can run"
  else
    warn "$tool: not found. Its recipe will skip, which is not the same as passing."
  fi
done

# Landlock is the layer that still applies on the host, so whether it works is
# the question this install most needs answered.
if [ -r /sys/kernel/security/lsm ] && grep -q landlock /sys/kernel/security/lsm 2>/dev/null; then
  ok "landlock: present in the kernel's LSM list"
else
  warn "landlock: not visible in /sys/kernel/security/lsm. 'le doctor' will confirm."
fi

if command -v bwrap >/dev/null 2>&1; then
  ok "bubblewrap: $(bwrap --version)"
else
  warn "bubblewrap: not found. DR-3 layer 3 will be unavailable."
fi

if [ "${missing}" -gt 0 ]; then
  say ""
  bad "${missing} required tool(s) missing; not installing."
  exit 1
fi

if [ "${CHECK_ONLY}" -eq 1 ]; then
  say ""
  say "Check only; nothing installed."
  exit 0
fi

# ---------------------------------------------------------------------------
# Build and install.
# ---------------------------------------------------------------------------
say ""
say "Building:"
mkdir -p "${bindir}" "${sharedir}"

# -trimpath and the version stamp match what deploy/Dockerfile does, so a host
# binary reports its provenance the same way an image one does.
version="$(git -C "${repo_root}" describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)"
commit="$(git -C "${repo_root}" rev-parse HEAD 2>/dev/null || echo unknown)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w"
ldflags="${ldflags} -X github.com/akynte/local-engineer/internal/version.Version=${version}"
ldflags="${ldflags} -X github.com/akynte/local-engineer/internal/version.Commit=${commit}"
ldflags="${ldflags} -X github.com/akynte/local-engineer/internal/version.Date=${date}"

(cd "${repo_root}" && CGO_ENABLED=0 go build -trimpath -ldflags "${ldflags}" -o "${bindir}/le" ./cmd/le)
ok "le -> ${bindir}/le"

# Profiles are embedded in the binary, so they need no install step. The
# sidecar is not: it is Node, and it has to live somewhere on disk.
if [ "${SKIP_SIDECAR}" -eq 0 ] && [ -f "${repo_root}/sidecars/typescript/package.json" ]; then
  say ""
  say "Installing the TypeScript sidecar:"
  mkdir -p "${sharedir}/sidecars/typescript"
  cp "${repo_root}/sidecars/typescript/package.json" \
     "${repo_root}/sidecars/typescript/package-lock.json" \
     "${repo_root}/sidecars/typescript/analyze.js" \
     "${sharedir}/sidecars/typescript/"
  (cd "${sharedir}/sidecars/typescript" && npm ci --omit=dev --no-audit --no-fund >/dev/null)
  # The same probe the image build runs: a sidecar that is present but cannot
  # produce a call edge is worse than an absent one, because the analyzer
  # treats absence as an ordinary condition and silently reads lexically.
  probe="$(mktemp -d)"
  printf 'export function f(): number { return 1; }\nexport function g(): number { return f(); }\n' > "${probe}/p.ts"
  if node "${sharedir}/sidecars/typescript/analyze.js" "${probe}" | grep -q '"kind":"calls"'; then
    ok "sidecar -> ${sharedir}/sidecars/typescript (probe produced a call edge)"
  else
    bad "the sidecar installed but produced no call edges; TypeScript will be read lexically"
  fi
  rm -rf "${probe}"
fi

# ---------------------------------------------------------------------------
# What to do next, including the parts that are not automatic.
# ---------------------------------------------------------------------------
say ""
say "Installed."
say ""
case ":${PATH}:" in
  *":${bindir}:"*) ;;
  *) warn "${bindir} is not on your PATH. Add it, or 'le' will not be found." ;;
esac

if [ "${SKIP_SIDECAR}" -eq 0 ] && [ -d "${sharedir}/sidecars/typescript/node_modules" ]; then
  say "For TypeScript analysis, export the sidecar location:"
  say ""
  say "  export LE_TYPESCRIPT_SIDECAR_DIR=${sharedir}/sidecars/typescript"
  say ""
fi

say "Then:"
say ""
say "  export LE_DATA=\"\${HOME}/.local/share/local-engineer\"   # not /data on a host"
say "  le doctor                                              # which layers are in effect"
say "  cd /path/to/repo && le workspace init && le index"
say ""
say "'le doctor' will warn that the container boundary is absent. That warning"
say "is correct and permanent for a host install; it is the trade you made."

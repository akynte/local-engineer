#!/usr/bin/env bash
# Exercise the split deployment (design v3 §4.3, DR-1).
#
# DR-1 chose a single container and promised a maintained multi-container
# alternative. Validating that file's syntax proves nothing about whether it
# works; what the layout actually claims is that running llama-server in its own
# container is "a configuration change only" — the supervisor sets
# inference.mode to external and talks to it over HTTP.
#
# That claim needs no GPU and no model, so CI can test it with a stub inference
# service. Inference itself is deliberately not tested here: a stub that
# answered completions would be testing the stub.
#
# Usage: LE_IMAGE=<image under test> scripts/check-split-layout.sh
set -euo pipefail

: "${LE_IMAGE:?set LE_IMAGE to the image under test}"
export LE_MODEL="${LE_MODEL:-placeholder.gguf}"

compose=(docker compose
  -f deploy/docker-compose.split.yml
  -f deploy/docker-compose.split.ci.yml)

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- inference logs ---"
    "${compose[@]}" logs --no-color --tail 40 inference || true
    echo "--- supervisor logs ---"
    "${compose[@]}" logs --no-color --tail 40 supervisor || true
  fi
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

echo "bringing up the split layout with a stub inference service"
"${compose[@]}" up -d --wait

# 1. The supervisor is ready. In external mode it manages no inference child,
#    so readiness must not depend on one.
ready=$(curl -fsS --max-time 10 http://127.0.0.1:7777/readyz)
echo "readyz: $ready"
echo "$ready" | grep -Eq '"ready"[[:space:]]*:[[:space:]]*true' || {
  echo "the supervisor did not become ready in the split layout" >&2
  exit 1
}

# 2. The configuration actually arrived. This is the claim: the deployment
#    points the supervisor at the inference container through the environment
#    alone. Both variables were set by the compose file and read by nothing,
#    which left a supervisor deployed this way in mode "none" with no base URL.
cfg=$("${compose[@]}" exec -T supervisor le config show)
mode=$(echo "$cfg" | sed -n 's/.*"Mode": *"\([^"]*\)".*/\1/p' | head -1)
base=$(echo "$cfg" | sed -n 's/.*"BaseURL": *"\([^"]*\)".*/\1/p' | head -1)

echo "inference mode: ${mode:-<empty>}"
echo "base URL:       ${base:-<empty>}"

[ "$mode" = "external" ] || {
  echo "inference.mode is ${mode:-<empty>}, want external: the supervisor is not" >&2
  echo "pointed at the inference container the layout deploys beside it" >&2
  exit 1
}
[ "$base" = "http://inference:8080" ] || {
  echo "inference.base_url is ${base:-<empty>}, want http://inference:8080" >&2
  exit 1
}

# 3. The profile the layout selects is the one meant for external inference.
echo "$ready" | grep -Eq '"profile"[[:space:]]*:[[:space:]]*"external-inference"' || {
  echo "the supervisor is not running the external-inference profile" >&2
  exit 1
}

echo "split layout: the supervisor runs external inference over HTTP, as DR-1 promised"

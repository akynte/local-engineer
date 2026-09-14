#!/usr/bin/env bash
# Verify that the workspace isolation tests actually fail when isolation is
# broken (design v3 §2.3).
#
# A test that cannot fail protects nothing, and an isolation test is exactly
# the kind that rots quietly: it keeps passing whether or not the code it
# guards still works.
#
# This deliberately breaks the cache's workspace binding, requires the test
# suite to notice, and restores the source. Crucially it also verifies that the
# mutation *applied* — otherwise a refactor that moves the patched line turns
# this check into a silent no-op that reports success forever.
set -euo pipefail

cd "$(dirname "$0")/.."

TARGET=internal/cache/cache.go
TEST='TestZeroCacheHitsAcrossWorkspaces'
BACKUP="$(mktemp)"
trap 'cp "$BACKUP" "$TARGET"; rm -f "$BACKUP"' EXIT

cp "$TARGET" "$BACKUP"

# The mutation: drop the workspace id from the cache key derivation. Two
# workspaces holding identical content would then compute the same key and hit
# each other's entries.
if ! grep -q 'c\.ws\.String(), namespace, manifest' "$TARGET"; then
  cat >&2 <<'MSG'
ERROR: the isolation mutation check could not find its patch site.

  Expected in internal/cache/cache.go:
      c.ws.String(), namespace, manifest

  The cache key derivation has been refactored. Update this script to break the
  new code, or the check silently stops verifying anything.
MSG
  exit 1
fi

sed -i 's/c\.ws\.String(), namespace, manifest/namespace, manifest/' "$TARGET"

# Confirm the mutation is really in the file before drawing any conclusion.
if grep -q 'c\.ws\.String(), namespace, manifest' "$TARGET"; then
  echo "ERROR: the mutation did not apply; this check is not verifying anything." >&2
  exit 1
fi

# It must still compile, or the test would fail for the wrong reason.
if ! go build ./internal/cache/ 2>/dev/null; then
  echo "ERROR: the mutated source does not compile; the check cannot distinguish" >&2
  echo "       a build failure from a detected isolation break." >&2
  exit 1
fi

if go test -count=1 -run "$TEST" ./internal/store/ >/dev/null 2>&1; then
  cat >&2 <<MSG
FAIL: $TEST passed with workspace isolation deliberately broken.

  The cache key no longer includes the workspace id, so two workspaces with
  identical content share cache entries — and the test did not notice.
  The isolation tests are not protecting the contract in design v3 §2.3.
MSG
  exit 1
fi

echo "confirmed: the isolation tests fail when isolation is broken"

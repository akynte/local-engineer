#!/usr/bin/env bash
# Record the README's 60-second demo as an asciinema cast (design v3 §15:
# "README with a 60-second demo (asciinema)").
#
# The cast is GENERATED from real command output, never hand-written. A
# hand-written cast is a screenshot of a system that may not exist any more,
# and this repository's whole argument is that claims are checkable. Every
# frame here comes from running the command inside the image and capturing what
# it actually printed.
#
#   scripts/record-demo.sh [image] > docs/demo.cast
#
# The output is asciicast v2 (https://docs.asciinema.org/manual/asciicast/v2/):
# a JSON header line, then [time, "o", data] event lines.
set -euo pipefail

IMAGE="${1:-local-engineer:cpu-test}"
COLS=100
ROWS=30

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "record-demo: no image named $IMAGE" >&2
  echo "  build one first: docker build -f deploy/Dockerfile --target cpu -t $IMAGE ." >&2
  exit 1
fi

# The demo runs entirely inside one container, against a throwaway repository
# it creates, so the cast never shows anything from the recorder's machine.
setup='
set -e
export LE_DATA=/tmp/led HOME=/tmp
mkdir -p /tmp/demo && cd /tmp/demo && git init -q .
cat > go.mod <<EOF
module example.com/shop

go 1.26
EOF
mkdir -p internal/pricing internal/orders
cat > internal/pricing/pricing.go <<EOF
package pricing

// Total sums a line total. Callers pass minor units.
func Total(unit, qty int) int { return unit * qty }
EOF
cat > internal/orders/orders.go <<EOF
package orders

import "example.com/shop/internal/pricing"

func LineTotal(unit, qty int) int { return pricing.Total(unit, qty) }
EOF
git add -A >/dev/null && git -c user.email=d@local -c user.name=demo commit -qm init
'

# Each entry is a prompt line and the command behind it. Keeping them apart is
# what lets the cast show a short prompt while running something longer.
run_in_container() {
  docker run --rm --entrypoint bash "$IMAGE" -c "$setup
cd /tmp/demo
$1" 2>&1 || true
}

# --- asciicast v2 -----------------------------------------------------------
now=0
emit() {
  # $1 = seconds to advance before printing, $2 = text. The text is passed as
  # an argument rather than on stdin so no trailing newline is invented, and
  # escape sequences are interpreted here rather than being written literally
  # into the cast.
  now=$(awk -v n="$now" -v d="$1" 'BEGIN{printf "%.6f", n + d}')
  python3 -c '
import json, sys
print(json.dumps([float(sys.argv[1]), "o", sys.argv[2]]))
' "$now" "$(printf '%b' "$2")"
}

python3 -c '
import json, time, sys
print(json.dumps({
    "version": 2, "width": int(sys.argv[1]), "height": int(sys.argv[2]),
    "timestamp": int(time.time()),
    "title": "local-engineer: index, impact, verify",
    "env": {"SHELL": "/bin/bash", "TERM": "xterm-256color"},
}))
' "$COLS" "$ROWS"

demo_step() {
  local prompt="$1" cmd="$2" pause="${3:-1.2}"
  emit 0.4 "\033[1;32m$\033[0m $prompt\r\n"
  local out
  out="$(run_in_container "$cmd")"
  # Carriage returns so a terminal replaying the cast renders line breaks.
  emit "$pause" "$(printf '%s' "$out" | sed 's/$/\r/')"
  emit 0.2 "\r\n"
}

demo_step "le workspace init" \
          "le workspace init" 0.8

demo_step "le index" \
          "le workspace init >/dev/null; le index" 1.5

demo_step "le graph impact Total --change signature" \
          "le workspace init >/dev/null; le index >/dev/null 2>&1; le graph impact Total --change signature" 1.5

demo_step "le task verify" \
          "le workspace init >/dev/null; le index >/dev/null 2>&1; le task verify" 2.0

emit 1.0 "\033[2m# every claim above came from running the command, not from a script.\033[0m\r\n"
emit 1.5 ""

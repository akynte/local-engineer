#!/usr/bin/env bash
# local-engineer container entrypoint (design v3 §4.3, §4.4).
#
# It prepares the data volume, reports which isolation layers the runtime
# actually permits, and then execs `le` so the supervisor is the process that
# receives SIGTERM and runs the orderly shutdown of §4.4.
set -euo pipefail

: "${LE_DATA:=/data}"

log() { printf '%s entrypoint: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# The data volume must be writable by the unprivileged user. A fresh named
# volume is created root-owned by the daemon, so say so clearly rather than
# failing later inside SQLite.
if [ ! -w "$LE_DATA" ]; then
  log "FATAL: $LE_DATA is not writable by uid $(id -u)."
  log "  Named volume: run 'docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data' once."
  log "  Bind mount:   chown the host directory to uid 10001, or pass --user \"\$(id -u):\$(id -g)\"."
  exit 1
fi

# Only the directories the supervisor owns. XDG directories are NOT created
# here: §2.2 sets them per task process, pointing at that workspace's own
# opencode/ directory, so the engine can never see another workspace's sessions.
mkdir -p "$LE_DATA"/config "$LE_DATA"/models "$LE_DATA"/workspaces "$LE_DATA"/backups

# SQLite needs a real filesystem with working fsync (§5.4). Warn loudly here;
# `le doctor` fails the check properly, but by then the user has already
# written data to a layer that will vanish.
fstype="$(stat -f -c %T "$LE_DATA" 2>/dev/null || echo unknown)"
case "$fstype" in
  overlayfs|overlay|aufs)
    log "WARNING: $LE_DATA is on $fstype, a container overlay layer."
    log "  Mount a named volume or a bind mount at $LE_DATA, or your data will be lost"
    log "  when the container is removed, and SQLite's fsync guarantees will not hold."
    ;;
  nfs|smb2|cifs)
    log "WARNING: $LE_DATA is on $fstype, a network filesystem. SQLite locking is unreliable there."
    ;;
esac

# Report the isolation layers the runtime actually permits (DR-3). This runs
# before the supervisor so a restrictive seccomp profile is visible in the
# first lines of `docker logs`.
if [ "${LE_SKIP_PREFLIGHT:-0}" != "1" ]; then
  le doctor 2>&1 | sed 's/^/  /' >&2 || true
fi

if [ ! -f "$LE_DATA/config/le.yaml" ]; then
  log "no configuration found; writing defaults to $LE_DATA/config"
  le config init >/dev/null
fi

log "starting: le $*"
exec le "$@"

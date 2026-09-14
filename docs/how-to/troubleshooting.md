# Troubleshooting

**Run `le doctor` first.** Most problems here are named directly by it, with
the fix. Exit status is 0 clean, 1 warnings, 2 failures.

```console
$ docker exec local-engineer le doctor
$ docker exec local-engineer le doctor --deep   # adds a full integrity check
$ docker exec local-engineer le doctor --json   # for a bug report
```

The JSON output contains no repository content, so it is safe to attach to an
issue.

---

## The container starts but nothing responds on 7777

Almost always the bind address. Inside the container the API must bind every
interface, because Docker's port publishing cannot reach a loopback bind there.
The published port is what restricts exposure.

The image sets `LE_API_ADDR=0.0.0.0:7777` for you. If you overrode it:

```console
$ docker exec local-engineer sh -c 'echo $LE_API_ADDR'
0.0.0.0:7777
```

And check you published it correctly — `-p 127.0.0.1:7777:7777`, not `-p 7777`.

## `/data is not writable by uid 10001`

A fresh named volume is created owned by root; the container is not root.

```console
$ docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data
```

Or run as yourself: `--user "$(id -u):$(id -g)"`.

## `data directory … is on overlayfs`

You did not mount a volume. Everything would be lost when the container is
removed, and SQLite's durability guarantees would not hold. This is a failure,
not a warning.

```console
$ docker run -d -v le-data:/data …
```

Network shares (NFS, SMB, sshfs) fail for a different reason: SQLite's locking
is unreliable on them.

## `bubblewrap … No permissions to create new namespace`

Expected, and not a problem. Unprivileged user namespaces are usually
unavailable inside a container, so the optional third isolation layer is off.
You still have the container boundary and per-task Landlock rules.

What you lose: concurrent tasks can see each other's processes.

To enable it anyway:

```console
$ docker run --security-opt seccomp=unconfined --security-opt apparmor=unconfined …
```

That weakens the container's own restrictions, so it is a trade, not a
straight win. On an Ubuntu host, `kernel.apparmor_restrict_unprivileged_userns`
is 1 by default and blocks this even outside a container.

## `landlock … probe failed`

The runtime's seccomp profile is blocking the three Landlock syscalls. Docker's
default profile permits them. If yours does not, layer 2 is unavailable and
`le doctor` says so rather than pretending otherwise.

```console
$ docker exec local-engineer le doctor --json | jq '.checks[] | select(.name|contains("landlock"))'
```

## `network containment` always warns

By design. Landlock's TCP rules do not cover Multipath TCP sockets, and Go's
`net.Listen` uses MPTCP by default, so a sandboxed Go program can still listen
on an unlisted port. The port rules are augmentation; the container's network
configuration and the allowlisting proxy are the boundary. The warning is
permanent so nobody builds a guarantee on top of the port rules alone.

## `no .le/workspace.yaml found`

You are not inside a workspace.

```console
$ cd /work/myproject && le workspace init
```

## The workspace id changed after I moved the directory

It should not have, if `.le/workspace.yaml` is committed. That file pins the
id. If it is present and you moved the directory:

```console
$ le workspace adopt
workspace … re-bound
  was: /old/path
  now: /new/path
```

The id never changes; adopt only refreshes the recorded derivation. If the file
was *absent*, `le workspace init` created a genuinely new workspace, which is
the documented behaviour — moving without the pin means "this is a new
project".

## `… belongs to workspace X but was opened as Y`

A database file is in the wrong workspace directory — usually a backup restored
into the wrong place. This is the isolation contract working: the file carries
the id of the workspace that created it, and serving you another project's code
would be worse than failing.

Restore into the right workspace, or re-index from scratch.

## Retrieval is missing something I expected

Look at what it actually returned before raising the packet budget:

```console
$ le graph search "the symbol you expected" --expand 2 --json
```

Check the index is current:

```console
$ le doctor --json | jq '.checks[] | select(.name=="index freshness")'
$ le index
```

If the symbol is indexed but not retrieved, that is a retrieval problem, not a
budget problem, and a bigger packet costs prefill time on every step without
fixing it.

## Impact analysis reports fewer consumers than I expect

The report is a lower bound and says so. A missing edge means the analysis did
not discover the relationship — dynamic dispatch, reflection, generated code,
string-built SQL, a language with no analyzer yet. Consumers reached by
`inferred` or `unknown` evidence *are* included and labelled; they are never
filtered out.

```console
$ le graph impact MySymbol --change signature --json | jq '.consumers[].evidence'
```

## A task will not resume after a crash

```console
$ le task recover
```

`safe: false` means an operation was partially applied and the worktree needs a
human look before anything replays. That is deliberate: replaying an edit whose
outcome is uncertain is how a half-applied change becomes a corrupted one.

## Still stuck

Open an issue with `le doctor --json`, `le version`, the image digest, and what
you expected. See [SUPPORT.md](../../SUPPORT.md).

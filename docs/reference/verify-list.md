# The v3 verify list

§16 of the design lists seven things marked `[VERIFY]` — assumptions that needed
checking against reality rather than being taken on trust. This page records
what was found.

An unchecked `[VERIFY]` is a claim the design makes and nobody tested. Leaving
the list untracked would make the label decorative.

| # | Item | Status |
|---|---|---|
| 1 | Landlock syscalls under the container runtime's default seccomp profile; Podman behaviour | **Checked (Docker)** |
| 2 | Unprivileged user namespaces inside the default container on Ubuntu 26.04 | **Checked — unavailable** |
| 3 | `opencode acp` transport options from outside the container | **Superseded by DR-7** |
| 4 | NVIDIA Container Toolkit and CUDA pairing for the pinned llama.cpp build | **Checked on the reference machine** |
| 5 | Image size after layering; whether Playwright belongs in its own layer | **Partly checked** |
| 6 | SQLite on a Docker named volume versus a bind mount | **Not checked** |
| 7 | sqlite-vec loadable-extension support in the chosen driver | **Not applicable yet** |

## 1. Landlock under the default seccomp profile

Works. `le doctor` reports **ABI 8 with TCP rules enforced** on kernel 7.0 under
Docker 29, with no seccomp flag added. The Go library degrades best-effort on
older ABIs.

Podman is **not verified** — nobody has run it there.

The honest limit is not seccomp but coverage: Landlock's network rules do not
cover Multipath TCP, and Go's `net.Listen` uses MPTCP by default. Port rules are
augmented, never relied on alone, and `le doctor` says so at runtime.

## 2. User namespaces inside the container

**Unavailable by default.** On Ubuntu 24.04 and later,
`kernel.apparmor_restrict_unprivileged_userns` is `1`, so AppArmor blocks
unprivileged user namespaces. `le doctor` reports the bubblewrap layer as
unavailable and names the flag that would enable it
(`--security-opt apparmor=unconfined`).

This decided the default: **bubblewrap is off**, and §6.2's table states plainly
that process-level isolation between concurrent tasks requires it.

## 3. ACP transport

**Superseded.** The item asks about `opencode acp`, and the engine decision
changed: [DR-7](../adr/0007-native-engine.md) supersedes DR-5, and what shipped
is a native engine that does not speak ACP. The `[VERIFY]` cannot be resolved
as written.

What exists instead is the ACP-over-TCP bridge of §4.3, which carries bytes
between a TCP connection and any configured agent's stdio without parsing the
protocol. It is off by default, because with a native engine there is nothing to
carry until an operator names an agent.

## 4. CUDA pairing

Verified on the reference machine only: **CUDA 13.3, driver 595.84, RTX 4060
8188 MiB**, with a llama.cpp CUDA build serving a 35B-A3B MoE at Q4_K_XL — 3.5 GB
VRAM with experts on CPU, ~416 tok/s prefill and ~35 tok/s decode.

One machine is not a compatibility matrix. ROCm is **not checked**.

## 5. Image size and layering

The CPU image is about **837 MB** ([manifest](image-manifest.md)), still well
under the 4–5 GB the design estimated for the CUDA variant — which is
unsurprising, since the estimate was for the image carrying the CUDA runtime.

That is 127 MB more than the 710 MB first measured, for twelve added tools:

| Addition | Why it is in the image |
|---|---|
| TypeScript sidecar | Without it TypeScript gets no call graph at all. |
| `golangci-lint` | §10.1's lint leg had no recipe to run. |
| `semgrep` | The analyzer recipe skipped on every published image. |
| `gosec`, `gitleaks`, `osv-scanner`, `buf`, `oasdiff`, `squawk` | Reachable through a `check:` step; the engine has no shell tool, so a tool absent here is unreachable. |

**It was 3,822 MB before the caches were cleaned.** The `go install` layer had
`rm -rf $GOPATH/pkg/mod/cache/download`, which removes the *download* cache and
leaves 3.2 GB of extracted modules — plus a second Go toolchain that `buf`
asked for. `go clean -cache -modcache -testcache` and removing `$GOPATH/pkg`
outright is what the layer does now. Worth recording because the difference
between the two commands is invisible in a Dockerfile review and costs 3 GB.

semgrep is the largest single item that remains (a Python virtualenv, ~175 MB,
with no released binary, so there is no smaller way to carry it). If image size
becomes the binding constraint it is the first thing to reconsider, and the
`-slim` variant is already the answer for anyone who wants none of this.

**Playwright is still not in any image, and the question is now sharper rather
than resolved.** The mechanism to drive a browser exists — an `integration:`
step in `.le/verify.yaml` runs one, with the ports it declares — so the
blocker is no longer "nothing could use it". It is that no image ships a UI for
a browser to drive, so ~300 MB of browsers would run nothing for every user who
is not testing a web front end. That is an argument for a separate layer or a
derived image, which is exactly what this item asks and what remains unsettled.

**Playwright is not in any image**, so whether it belongs in its own layer has
not been settled. It becomes a real question when runtime UI verification is
built.

## 6. SQLite on a named volume versus a bind mount

**Not checked.** `evals/storage/` measures SQLite performance but has only been
run on the host filesystem, not across the two Docker storage shapes.

What *is* enforced is the thing the question is really about: `le doctor` fails
when the data directory is on an overlay layer or a network share, because
SQLite needs working `fsync`.

## 7. sqlite-vec

**Not applicable.** Semantic retrieval is optional and gated on evidence that it
earns its cost, which the evaluation has not produced. The extension is not
loaded, so driver support has not been tested. If semantic retrieval is ever
built, this becomes a real question first.

## Keeping this current

Anyone resolving one of these should edit this page in the same change. A verify
list that records an answer from a year ago and does not say so is worse than no
list.

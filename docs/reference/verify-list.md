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
| 3 | `opencode acp` transport options from outside the container | **Superseded by DR-5** |
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

**Superseded.** The item asks about `opencode acp`, and DR-5 deviated: what
shipped is a native engine that does not speak ACP. The `[VERIFY]` cannot be
resolved as written.

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

The CPU image is about **700 MB** ([manifest](image-manifest.md)), well under
the 4–5 GB the design estimated for the CUDA variant — which is unsurprising,
since the estimate was for the image carrying the CUDA runtime.

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

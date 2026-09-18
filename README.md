<div align="center">

# local-engineer

**A supervised coding engineer that runs entirely on your machine.**

[![CI](https://github.com/akynte/local-engineer/actions/workflows/ci.yml/badge.svg)](https://github.com/akynte/local-engineer/actions/workflows/ci.yml)
[![CodeQL](https://github.com/akynte/local-engineer/actions/workflows/codeql.yml/badge.svg)](https://github.com/akynte/local-engineer/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/akynte/local-engineer/badge)](https://scorecard.dev/viewer/?uri=github.com/akynte/local-engineer)
[![Go Reference](https://pkg.go.dev/badge/github.com/akynte/local-engineer.svg)](https://pkg.go.dev/github.com/akynte/local-engineer)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[Quickstart](#quickstart) · [How it works](#how-it-works) ·
[Limitations](#limitations-read-this) · [Docs](docs/index.md) ·
[Architecture](local-coding-system-review.md)

</div>

---

A deterministic harness wraps a local model. The repository is indexed into a
typed code graph, every action is journalled before it happens, verification
runs in a sandbox, and nothing reaches your working tree until a person has read
a diff.

**The model is the least trusted component in the system.** It cannot run a
shell, reach the network, write a file the plan did not declare, or decide that
its own work is finished. See [trust boundaries](docs/explanation/trust-boundaries.md).

It is also replaceable. Any OpenAI-compatible endpoint works — the reference
setup runs [Ternary Bonsai 2 27B](https://huggingface.co/prism-ml/Ternary-Bonsai-2-27B-gguf)
at **29 tok/s on an 8 GB laptop GPU**, measured, because 1.72 bits/weight puts a
27B-class model entirely in VRAM. Swap it for another in two lines of config.

> [!IMPORTANT]
> **Status: pre-1.0.** The pipeline works end to end: a task localizes, plans,
> edits a confined worktree, verifies in a sandbox, is reviewed in a fresh
> context, and commits behind a human gate. What does **not** exist is a task
> set large enough to prove it helps, so this README publishes no success
> rate. Read
> [Limitations](#limitations-read-this) before adopting it — it is the longest
> section on purpose.

---

## Quickstart

Five steps, once. After them you work in your editor, not in this CLI.

### 1 — Install `le`

Needs [Go](https://go.dev/dl/) 1.26+ and a C compiler (`build-essential` on
Debian/Ubuntu, Command Line Tools on macOS).

```bash
git clone https://github.com/akynte/local-engineer
cd local-engineer
make install                      # builds ./bin and installs `le` into $GOBIN
```

Add `$GOBIN` (usually `~/go/bin`) to your `PATH` if it is not there, then:

```bash
echo 'export LE_DATA="$HOME/.le"' >> ~/.bashrc && export LE_DATA="$HOME/.le"
le config init
```

### 2 — Serve a model

The model is a boundary, not a dependency: **any OpenAI-compatible endpoint
works**, and nothing in the supervisor knows which engine is behind it. Two
lines of configuration change it.

#### The reference model

[**Ternary Bonsai 2 27B**](https://huggingface.co/prism-ml/Ternary-Bonsai-2-27B-gguf)
— Qwen3.8-27B packed to 1.72 bits/weight, Apache-2.0, **5.95 GB**, 405k
downloads in the 30 days to 2026-09-18.

It is why a 27B-class model is usable on this hardware at all: it fits an 8 GB
card whole, so no layer streams over PCIe and the CPU stays out of the decode
loop entirely.

Measured here with `llama-bench` on an RTX 4060 Laptop (8 GB), all 64 layers
resident. These are not vendor figures:

| | |
|:--|--:|
| decode | **29.0 tok/s** |
| prompt processing | **285.8 tok/s** |
| resident | 5.95 GB of 8188 MiB VRAM |

```bash
# needs PrismML's llama.cpp fork — see the note below
llama-server -m Ternary-Bonsai-2-27B-PTQ1_0.gguf \
  -c 32768 -ngl 99 -fa on --jinja \
  --temp 1.0 --top-p 0.95 --top-k 20 \
  --host 127.0.0.1 --port 8080
```

> [!WARNING]
> **Bonsai needs [PrismML's llama.cpp fork](https://github.com/PrismML-Eng/llama.cpp).**
> Stock llama.cpp rejects `PTQ1_0` as an unknown type, and — worse — loads a
> plain `Q2_0` without complaint and produces garbage, because it has no
> Hadamard activation runtime. Build it with `-DGGML_CUDA=ON`.
> Its quality claims (98.2% of FP16) are the vendor's, measured on H100s, and
> are not reproduced here. What *is* reproduced here is the table above.

#### Or any other model

A stock `llama-server`, vLLM, SGLang, Ollama, LM Studio, or a remote API.
Nothing above is required:

```bash
llama-server -m /path/to/your-model.gguf \
  -c 32768 -ngl 99 -fa on --jinja --host 127.0.0.1 --port 8080
```

Then point `le` at whatever you chose — `~/.le/config/le.yaml`:

```yaml
inference:
  mode: external
  base_url: http://127.0.0.1:8080
profile: bonsai-2-27b-8gb-cuda     # or reference-8gb-cuda-64gb-ram, or your own
```

and the model name in `~/.le/config/providers.yaml` under `model:`. Swapping
models later means editing those two files and re-running
`le opencode setup`, which re-points the editor at the new one.

```bash
le models conformance     # does this model really do tool calls and JSON schemas?
```

**Run this before anything else, whichever model you chose.** The pipeline needs
both capabilities, and a model that lacks either fails later, deep inside a
phase, with an error about something else. `le models bench --write` then
measures your machine and writes a profile from what it saw, replacing the
shipped estimates.

### 3 — Install OpenCode

```bash
curl -fsSL https://opencode.ai/install | bash
```

### 4 — Point `le` at your project

```bash
cd ~/code/my-project
le workspace init      # pin this repository's identity
le index               # build the code graph — minutes on a large repo, once
le opencode setup      # register the tools, wire the model, write AGENTS.md
```

`setup` writes `opencode.json` and an `AGENTS.md` block. Commit both: they carry
no machine-specific paths, and they mean a colleague who clones the repository
gets the same setup.

### 5 — Work

```bash
le opencode run
```

That opens an ordinary OpenCode session — ask a question, ask for a feature,
paste a stack trace. What is different is underneath: the session is confined to
this repository, and the supervisor's tools are in front of it.

| Ask for | What happens |
|:--|:--|
| "how does auth work here?" | answered from the compiler-backed graph, not from grep |
| "what breaks if I change this signature?" | `le_graph_impact` lists every consumer and why each was found |
| "fix the failing test" | `le_read`/`le_edit` apply the path policy; `le_verify` decides whether it worked |
| "we always retry with backoff here" | `le_note_add` records it, and the next session starts knowing |

`le_verify` runs your repository's own checks in a sandbox and reports
ACCEPTED or the failures. The model does not get to declare success.

> [!TIP]
> Plain `opencode` also works after step 4 — same tools, same context. The
> difference is that `le opencode run` confines the session: no home directory,
> no inherited environment, no shell, no network beyond the model. Use it when
> the session is going to edit rather than answer.

Prefer a supervised, gated pipeline over an editor session? That is the
`le task` flow — [CLI reference](docs/reference/cli.md).

---

## How it works

Two problems sink most local coding assistants: a context window too small for a
real codebase, and a model that cannot hold one in its head. Both are attacked
from **outside** the model.

### The pipeline

Eight phases, persisted in SQLite, each with its own tools, budget and exit
condition. A crash resumes at the recorded phase.

| Phase | Who runs it | What has to be true to leave |
|:--|:--|:--|
| **INTAKE** | supervisor | verification commands frozen, index fresh, baseline recorded |
| **LOCALIZE** | 3 structured calls | confirmed symbols and a written hypothesis |
| **IMPACT** | no model | consumers, contracts, tests and migrations computed from the graph |
| **PLAN** | 1 structured call | plan validates: files, tests, write allowlist, every obligation resolved |
| **EDIT** | bounded tool loop | model declares done **and** the gates pass |
| **VERIFY** | no model | every frozen check passes on *this* content hash |
| **REVIEW** | fresh context | a structured verdict accepts, or routes to bounded repair |
| **FINALIZE** | supervisor | secret scan clean, human gate answered, commit on a task branch |

### The system

```
                          USER (terminal)
                                │
┌───────────────────────────────▼──────────────────────────────────┐
│  SUPERVISOR — one Go binary, one process                         │
│                                                                  │
│  Ledger (SQLite) · Phase machine · Budgets · Failure controller  │
│        │                    │                                    │
│  ┌─────▼──────────┐  ┌──────▼────────┐                           │
│  │ CONTEXT PACKER │  │ TOOL FIREWALL │                           │
│  │ frozen prefix  │  │ schema · phase│                           │
│  │ append-only log│  │ path · limits │                           │
│  └─────┬──────────┘  └──────┬────────┘                           │
│        │                    │                                    │
│  ┌─────▼────────────────────▼─────────────────────────────────┐  │
│  │ CODE INTELLIGENCE — deterministic, no model                │  │
│  │   SCIP index    compiler-produced cross references         │  │
│  │   tree-sitter   Go · Rust · TypeScript/TSX                 │  │
│  │   LSP client    live answers for files an edit changed     │  │
│  │   ripgrep       200 ms budget, result caps                 │  │
│  │   repo map      PageRank over the reference graph          │  │
│  │   impact        callers, impls, tests, schemas, migrations │  │
│  └─────┬──────────────────────────────────────────────────────┘  │
│        │                                                         │
│  ┌─────▼──────────┐        ┌────────────────────────────┐        │
│  │ MODEL GATEWAY  │───────▶│ llama.cpp                  │        │
│  │ OpenAI-compat  │        │ one slot, profile swap     │        │
│  └─────┬──────────┘        └────────────────────────────┘        │
│        │                                                         │
│  ┌─────▼──────────┐        ┌────────────────────────────┐        │
│  │ SANDBOX        │───────▶│ VERIFICATION               │        │
│  │ bwrap+landlock │        │ frozen presets, repo-native│        │
│  │ no net, secrets│        │ per-test results           │        │
│  └────────────────┘        └────────────────────────────┘        │
│                                                                  │
│  MEMORY (.agent/, versioned)      TRACE (SQLite + JSONL)         │
└──────────────────────────────────────────────────────────────────┘
```

### The five bets

**Questions are answered before the model sees anything.** Symbol locations,
callers, implementations and test results come from a compiler-backed index, not
from the model's recollection. The context window stops being the binding
constraint because the supervisor hands over only the slice the step needs.

**Every prompt is a frozen prefix plus an append-only log.** For the hybrid
attention models this targets, a prompt is cheap only as an exact prefix
extension of the last one — any edit earlier in the prompt costs a full
re-prefill, minutes on a laptop GPU. So nothing is ever reordered or dropped
mid-phase; a full log is a phase boundary, not a licence to discard evidence.

**Work survives interruption.** Intent is journalled *before* each side effect
and the outcome *after*. A crash leaves an operation with no outcome, and
recovery resolves it by inspecting the worktree rather than assuming.

**A model's claim of success decides nothing.** A task is accepted only when
every required check passes against the current content hash. A skip, an error,
or a pass against an older state satisfies nothing — then a human reads the diff.

**Breaking a caller is accounted for, not mentioned.** A signature change
derives its consumers from the graph and the plan must resolve each one — an
edit, or "no change needed" with a concrete reason. EDIT cannot declare itself
done while an obligation is open.

---

## Running it in Docker instead

The Quickstart installs on the host, which is the shortest path to a working
editor session. Docker is the alternative, and it is the only way to get the
container boundary — DR-3 layer 1, which a host install does not have.

```bash
docker volume create le-data
docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data

docker run -d --name local-engineer \
  --gpus all \
  -v le-data:/data \
  -v "$HOME/code":/work \
  -p 127.0.0.1:7777:7777 \
  ghcr.io/akynte/local-engineer:latest
```

Omit `--gpus all` on a CPU-only host. **Keep the `127.0.0.1` prefix** — the
supervisor runs sandboxed commands and reads every indexed repository, so it
must not be reachable from your network.

```bash
docker exec -it local-engineer le doctor
docker exec -it local-engineer bash -c 'cd /work/my-project && le workspace init && le index'
```

`docker compose -f deploy/docker-compose.yml up -d` wraps the same thing, and
[install on the host](docs/how-to/install-on-the-host.md) spells out what
isolation each choice costs you.

Editor sessions are the awkward case in a container: OpenCode runs on your host,
so it needs `le` on your host too. Either install both on the host as the
Quickstart does, or keep the container for indexing and verification and run
`le opencode run` outside it against the same `LE_DATA`.

---

## `le doctor`

The first command to run and the one to attach to a bug report. It reports what
is *actually* in effect, never what the design hopes for:

```
[warn] container boundary (DR-3 layer 1)  no container marker found; the container
                                          boundary of DR-3 layer 1 is NOT in effect
[ok  ] landlock (DR-3 layer 2)            ABI 8; TCP rules enforced
[ok  ] bubblewrap (DR-3 layer 3)          available: mount and PID namespaces active
[warn] network containment                Landlock TCP rules do not cover Multipath TCP…
[ok  ] data directory                     ~/.le on ext4
[ok  ] hardware profile                   bonsai-2-27b-8gb-cuda: context 32768, packet cap 12000
[warn] profile fits host                  the profile carries no measurement, so
                                          admission limits are estimates
[ok  ] index freshness                    last indexed 11m0s ago; 10 nodes, 11 edges
```

That is a host install with no container, which is why layer 1 warns. Nothing is
rounded up: the node count is small because the repository in that run is small.

`le trace <task>` answers the other question — *why did it change that file?* —
as the chain of operations that led there. `--jsonl` exports it.

---

## Limitations, read this

The complete list is [known limitations](docs/explanation/known-limitations.md).
These are the ones most likely to change your mind.

### It is not proven to help

- **No success rate is published, because none is trustworthy yet.** A real run
  exists — 3 tasks × 4 arms × 5 passes — but every arm's confidence interval
  overlaps every other's, and 4 of 12 task/arm cells changed verdict between
  passes. The graph's contribution is **measurable, not measured**.
- **The component ladder has not run.** The architecture prescribes measuring
  each layer's marginal value and deleting what does not pay. That has not
  happened, so every claim here about *why* a layer exists is a design argument,
  not evidence.
- **The context packer's justification is unmeasured on real tasks.** Exact
  prefix reuse is verified against a live server; that it saves you time across
  a whole task is not.

What the evaluation *does* show is that false acceptance is real: the
unsupervised baseline claimed success on work that failed the hidden test in 4
of 15 runs. [The results](docs/benchmarks/results/).

### It is slow, and the spread is wide

- **Local inference is the floor.** On a laptop GPU a 27B-class model decodes at
  roughly 28 tok/s, and a structured phase call is tens of seconds. This suits
  bounded work where a verification gate is worth the wall time, not
  interactive pairing.
- **Run-to-run variance is large, because reasoning length is.** The same
  one-line fix took **160 seconds** in one run and **784 seconds** in another —
  same model, same task, same machine, a 4.9× spread. A single PLAN call in the
  slow run generated 5,300+ tokens of reasoning before answering. At the
  sampling temperature these models recommend, how long a task takes is not
  something you can predict from the task.
- **A reasoning model can spend an entire output budget thinking** and return
  nothing at all. Truncation is reported as its own outcome rather than as the
  model deciding to stop — but the budget is yours to set, and setting it too
  low turns a slow phase into a failed one.
- **Indexing a large monorepo is not instant**, and the first index of an
  unfamiliar repository is the slowest thing you will do.

### Sandbox and isolation

- **Binding TCP port 0 is denied inside the sandbox**, so `httptest`-style test
  suites fail with `bind: permission denied` on a port nobody chose. This is a
  known open bug, not a design choice: a specific ephemeral port binds fine, and
  the captured ruleset replays correctly in isolation, so the cause is in the
  execution path and is not yet found. Repos with HTTP tests will fail
  verification for a reason unrelated to the change.
- **Process isolation between concurrent tasks needs bubblewrap**, usually
  unavailable inside a container. Without it, concurrent tasks share a PID view.
  Isolation from your host and between workspaces does not depend on it.
- **Only bubblewrap can enforce "no network".** With Landlock alone it is TCP
  port rules, which do not cover UDP, raw sockets or Multipath TCP.
- **Out-of-scope writes inside a worktree are caught by diff and policy, not by
  the sandbox** — the task legitimately has write access to what it edits.
- **Prompt injection is not solved.** The defence is capability containment — no
  shell, no network, no path outside the plan, fresh-context review — not the
  delimiters around repository text. Those are a mitigation and are documented
  as one.

### Language coverage is uneven

- **Go is deepest.** Signature comparison uses `go/parser`; everything else uses
  tree-sitter.
- **Vue single-file components have no signature checking.** The published
  grammar has no Go module. A `.vue` file is reported as *unexaminable* rather
  than as clean — but it means obligations do not cover it.
- **TypeScript has no call graph without the Node sidecar.** The `cpu` and
  `cuda` images carry it; `-slim` and host installs without Node do not.
- **Dynamic SQL, computed routes and Helm templates produce no edge.** A guessed
  table or endpoint is worse than a missing one, and the report says "not
  discovered", never "does not exist".

### Build and operations

- **No static binary.** tree-sitter is cgo, so `le` links against the C library
  of the machine that built it, cross-compilation needs a toolchain per target,
  and `govulncheck` cannot see into the grammars' generated C.
- **SQLite needs a real filesystem** with working `fsync` — not an overlay, not
  a network share. `le doctor` fails if you use one.
- **Migrations are forward-only.** `le backup` before every upgrade.
- **Shipped hardware profiles are starting points, not measurements.** Run
  `le models bench --write`; `le doctor` warns until you do.
- **One model slot.** Switching profiles unloads the previous process. Two
  resident models do not fit the hardware this targets — so "use the small model
  for edits and the big one for review" costs a model load between them.
- **The reference model needs a llama.cpp fork.** Bonsai's ternary packings are
  not in stock llama.cpp, which rejects them outright and produces garbage from
  a plain `Q2_0` of the same weights. Any other GGUF avoids this entirely; the
  fork is a cost of that model, not of this project.

### Unsettled by design

- **Two editors exist.** A native tool loop and a confined OpenCode session. The
  architecture says to compare them on a task set and keep whichever wins; that
  comparison has not run, so both ship and neither is claimed better.
- **Embeddings and a reranker are deliberately absent** until a benchmark shows
  they earn their cost.

---

## Documentation

| | |
|:--|:--|
| **Tutorials** | [First task in 15 minutes](docs/tutorials/first-task.md) · [Index a Go microservice](docs/tutorials/index-a-go-service.md) · [Run the evaluation](docs/tutorials/run-the-evaluation.md) |
| **How-to** | [install](docs/how-to/install.md) · [use with OpenCode](docs/how-to/use-with-opencode.md) · [configure models](docs/how-to/configure-models.md) · [choose a profile](docs/how-to/choose-a-profile.md) · [run offline](docs/how-to/run-offline.md) · [back up and restore](docs/how-to/backup-and-restore.md) · [troubleshooting](docs/how-to/troubleshooting.md) |
| **Reference** | [CLI](docs/reference/cli.md) · [configuration](docs/reference/configuration.md) · [graph schema](docs/reference/graph-schema.md) · [HTTP API](docs/reference/http-api.md) |
| **Explanation** | [architecture](docs/explanation/architecture.md) · [implementation status](docs/explanation/architecture-implementation.md) · [isolation model](docs/explanation/isolation-model.md) · [trust boundaries](docs/explanation/trust-boundaries.md) · [known limitations](docs/explanation/known-limitations.md) · [decision records](docs/adr/) |

The architecture source of truth is
[local-coding-system-review.md](local-coding-system-review.md); the
[implementation status](docs/explanation/architecture-implementation.md) page
tracks what is built against it, including the boxes that differ. Older
`design v3` references in code describe historical decisions and do not override
the review.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md). Run `make check` before opening a pull
request. Security issues go through
[private reporting](https://github.com/akynte/local-engineer/security/advisories/new),
never a public issue — see [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

# Independent Architecture Review: Fully Local Coding System on RTX 4060 8 GB / 64 GB RAM

Review date: 17 September 2026
Reviewer role: second, independent architect (to be reconciled with the first model's proposal)
Target machine: Intel i7-13620H (6P+4E), 64 GB DDR5-5200 dual channel (about 61 GiB usable), RTX 4060 Laptop 8 GB, NVMe, Ubuntu, llama.cpp with CUDA

Evidence labels used throughout:
- VERIFIED: official model card, official repo, merged code, or your own measurement
- VENDOR: number published by the model vendor (self-reported, own scaffold)
- ANECDOTAL: community measurement on comparable hardware, not reproduced
- ESTIMATE: my calculation from architecture facts or from your own measurements; must be confirmed in Phase 0
- INSUFFICIENT EVIDENCE: I could not find reliable data

---

## 1. EXECUTIVE VERDICT

The proposed direction is correct: one capable model, deterministic tools for facts, explicit phases, verification that the model cannot bypass. I keep that skeleton. But the proposal contains four mistakes that would hurt in practice, and several layers that should not be built until a benchmark says they earn their cost.

Largest mistakes:

1. The proposal ignores the most important inference fact about the chosen model family. Every Qwen model from 3.5 onward (3.6-35B-A3B, 3.6-27B, 3.8-27B, Qwen3-Coder-Next, 3.8-Flash-Next) uses a hybrid Gated DeltaNet + attention layout. In llama.cpp these hybrid models cannot do partial KV reuse (`--cache-reuse` is disabled for them), and exact-prefix reuse depends on context checkpoints that have a long bug history (issues #19794, #19858, #20225, #22384, #22746, #24055, #24587). Your own measurement shows exact-prefix caching does work on your build for Qwen3.8-27B (14,416 of 14,447 tokens cached), which is good news, but it only works when the prompt is an exact prefix of the previous one. The proposed "context engine rebuilds a fresh context pack for every model call" therefore forces a full re-prefill on every call. On this GPU that is minutes per call. The context architecture has to be redesigned as "frozen prefix + append-only log", with re-packing allowed only at phase boundaries. This is the biggest change in my review.

2. The model plan is stale. Qwen3.6-27B has been replaced by Qwen3.8-27B (Apache 2.0, 14 Aug 2026). For the MoE tier there is still no newer 30-40B-class Qwen MoE; Qwen3.6-35B-A3B remains the right daily class. Qwen3.8-Flash-Next (177B total with the n-gram table, 6B active) is exciting but on 8 GB VRAM + 64 GB RAM it is an experiment, not a daily driver: the 46 GB n-gram table has to page from NVMe, and the one 64 GB report with a single 16 GB GPU shows prefill of 15-20 tok/s, which makes a 30K-token agent prompt take 25-30 minutes.

3. Too much retrieval machinery on day one. Embeddings and a reranker are the two components with the weakest evidence for repository tasks and the highest operational cost (a second and third model, index maintenance, invalidation). The Agentless result (hierarchical localization with a repo structure skeleton, no embeddings, no agent) and later work show that structure-first localization is the strong baseline. Embeddings and reranking become OPTIONAL, added only if the A/B benchmark shows a recall gain.

4. The proposal trusts the coding shell's permission system as part of the firewall. OpenCode's permission and plugin-hook enforcement has had documented bypass bugs (subagents bypassing `tool.execute.before` hooks, SDK ignoring agent deny lists, custom agent denies ignored). The firewall must therefore live outside the shell: an OS sandbox plus the supervisor's own policy checks. The shell's permission config is a convenience layer, not a security boundary.

Unnecessary complexity to remove: LangGraph or PydanticAI (a phase machine is 300 lines of Go), Mem0/Zep/Graphiti (files + SQLite), Serena as the long-term structural layer (fine for prototyping, but the supervisor should own LSP and SCIP directly), a 12-14 state machine (6 phases are enough), a separate reviewer model by default (same model, fresh context, is the default; the dense 27B review is an optional slow path).

What to keep unchanged: deterministic verification gates, failure fingerprinting, plan-before-edit, repository isolation, plain-file memory with recompute-from-repo rule, one model instead of a crew of agents, SQLite/JSONL tracing.

---

## 2. MODEL SELECTION

### 2.1 Landscape as of September 2026 (what changed since the proposal)

- Qwen3.6-35B-A3B: released April 2026, Apache 2.0. VERIFIED from the model card: 35B total / 3B active, 40 layers laid out as 10 x (3 x Gated DeltaNet + 1 x Gated Attention), 256 experts with 8 routed + 1 shared active, full-attention layers use 16 Q heads / 2 KV heads / head dim 256, 262,144 native context, MTP head trained, thinking mode default, "preserve_thinking" option. VENDOR benchmarks (200K context, their own bash + file-edit scaffold): SWE-bench Verified 73.4, SWE-bench Multilingual 67.2, SWE-bench Pro 49.5, Terminal-Bench 2.0 51.5, NL2Repo 29.4. Source: https://huggingface.co/unsloth/Qwen3.6-35B-A3B-MTP-GGUF (mirrors the official card). GGUF sizes (Unsloth): UD-IQ4_XS 18.2 GB, UD-Q4_K_XL 22.9 GB, UD-Q5_K_XL 27.2 GB, UD-Q6_K 30 GB, Q8_0 37.8 GB.
- Qwen3.8-27B: released 14 Aug 2026, Apache 2.0, dense, native vision, 64 layers, hidden 5120, hybrid Gated DeltaNet + Gated Attention, 262K native. VENDOR: SWE-Bench Pro 61.7, DeepSWE 1.1 42.2, LiveCodeBench 90.3. Source: https://huggingface.co/Qwen/Qwen3.8-27B. This is the model you already run at Q8_0 (VERIFIED by you: 2.9 tok/s short context, 1.79 tok/s at 27.5K, prompt processing about 240 tok/s).
- Qwen3.8-Flash-Next: released 26 Aug 2026, Qwen Community License 1.0 (not Apache; check terms for redistribution), 125B main model + 51B n-gram embedding table (176.9B in GGUF metadata), 6B active, GDN + Qwen Sparse Attention, 262K native. VENDOR: SWE-bench Pro 62.5, DeepSWE 1.1 58.7. llama.cpp mainline support merged 27 Aug 2026 (PR #27742). GGUF sizes: UD-IQ3_XXS 76 GiB (46 GB of it is the n-gram table), UD-Q2_K_XL 73 GB (27 GB table), UD-Q4_K_XL about 104 GiB. Sources: https://huggingface.co/Qwen/Qwen3.8-Flash-Next , https://huggingface.co/unsloth/Qwen3.8-Flash-Next-GGUF/discussions/3
- GLM-5.3-Flash: 320B / 18B active; 1-bit quant is 93 GB. Out of reach on this machine. Excluded.
- Qwen3-Coder-Next (Feb 2026): 80B / 3B active, Apache 2.0, 262K, about 42-49 GB at Q4. Still a serious candidate for a 64 GB box, but it is an older generation than Qwen3.6 and shares the same hybrid-cache problem. VENDOR SWE-bench numbers were strong in February; I found no independent comparison against Qwen3.6-35B-A3B. INSUFFICIENT EVIDENCE to prefer it; keep as a Phase 0 candidate only if the 35B-A3B benchmark disappoints.
- Gemma 4 26B-A4B: Qwen's own table lists it at 17.4 SWE-bench Verified under Qwen's scaffold. That is a hostile measurement, but it is the only cross-model number in one scaffold that I found. Not a coding-agent candidate.
- Community sentiment (ANECDOTAL, Hacker News and HF discussions): Qwen3.6-35B-A3B is widely used through OpenCode on 24 GB cards and is described as a "workhorse"; critics report it "getting lost as the task requires more steps" on long agent runs. That criticism is exactly what the supervisor design is for.

### 2.2 Candidate table

| Candidate | Arch | Total / active | Quant and file size | Placement on this machine | Expected bottleneck | Speed | Coding evidence | License | llama.cpp |
|---|---|---|---|---|---|---|---|---|---|
| A. Qwen3.6-35B-A3B | hybrid GDN + attn, MoE | 35B / 3B | UD-Q4_K_XL 22.9 GB (or UD-IQ4_XS 18.2 GB) | attention, shared expert, embeddings, KV on GPU (about 3-4 GB); routed experts in RAM (about 19 GB) | RAM bandwidth for decode; PCIe streaming of expert weights during prefill | ESTIMATE 12-25 tok/s decode, several hundred tok/s prefill (see 3.4) | VENDOR SWE-V 73.4, TB2 51.5; ANECDOTAL strong in OpenCode | Apache 2.0 | mainline, MTP draft supported |
| B. Qwen3.8-27B | hybrid GDN + attn, dense | 27B / 27B | UD-Q4_K_XL about 16-17 GB; Q8_0 28-29 GB (yours) | about 5-6 GB of layers on GPU, rest in RAM | RAM bandwidth (every token reads all weights) | VERIFIED at Q8: 2.9 tok/s; ESTIMATE at Q4: 4-6 tok/s; prefill about 240 tok/s at 27K (yours) | VENDOR SWE-Pro 61.7 | Apache 2.0 | mainline |
| C. Qwen3.8-Flash-Next | GDN + QSA, MoE + n-gram table | 177B / 6B | UD-IQ3_XXS 76 GiB, UD-Q2_K_XL 73 GB | trunk on GPU, experts in RAM, n-gram table paged from NVMe | NVMe page faults during prefill; CPU | ANECDOTAL (16 GB GPU + 64 GB RAM, IQ3_XXS): 25-29 tok/s decode, 15-20 tok/s prefill | VENDOR SWE-Pro 62.5; ANECDOTAL "very similar to Qwen3.8-27B" in agentic use | Qwen Community 1.0 | mainline since 27 Aug 2026, fast-moving, fork-only optimizations (expert LRU cache PR #27861) |
| D. Qwen3-Coder-Next | hybrid, MoE | 80B / 3B | Q4 about 42-49 GB | same as A but 2x the expert bytes | RAM capacity (49 GB + KV + OS is tight on 61 GiB) | INSUFFICIENT EVIDENCE for this box | VENDOR strong (Feb 2026) | Apache 2.0 | mainline |

### 2.3 Why A wins for daily work

- It is the only candidate that leaves headroom: about 23 GB of RAM for the model and 38 GB free for LSP servers, SCIP indexes, the build, tests, and Docker.
- 3B active parameters at Q4 means each decode token reads about 1.5-2 GB from RAM; at a realistic 50-60 GB/s that gives a ceiling around 25-35 tok/s, and community numbers on stronger desktops (RTX 3090, all in VRAM) are 80-120 tok/s. Your dense 27B reads 16-28 GB per token and is capped near 3-5 tok/s no matter what you tune. For an agent loop with hundreds of tool calls per task, that difference is decisive.
- Its vendor SWE-bench Verified score (73.4) is above the dense Qwen3.6-27B's peers in the same table and only a few points below Qwen3.8-27B on Pro-class tasks. The supervisor closes more of that gap than switching models would.
- Apache 2.0, MTP draft supported in llama.cpp for a real speedup at no quality loss (Unsloth reports 1.5-2x; treat as ANECDOTAL until you measure).

Why B stays: it is the strongest model you can actually run, it is already installed and measured, and it is the right tool for two non-loop jobs: the final diff review and hard planning on a task the MoE failed twice. At 4-6 tok/s it cannot be in the tool loop.

Why C is a watch item, not a plan: quality per byte is real, but on this machine every prefill hits the NVMe for the n-gram table. Revisit when (a) the expert LRU cache and MTP GGUF export land in mainline, and (b) someone reports prefill above 200 tok/s on a 64 GB box with one small GPU. Budget one evening for a measurement, nothing more.

Quantization choice for A: UD-Q4_K_XL by default. UD-IQ4_XS saves 4.7 GB of RAM at a small quality cost; use it only if RAM pressure appears with big builds. Do not go below Q4 for a coding model; small quant errors show up as wrong edits, not as prose quality loss.

---

## 3. INFERENCE CONFIGURATION

### 3.1 Runtime: llama.cpp (llama-server), CUDA build from master

Reasons, hardware-specific:
- vLLM and SGLang need the whole model in VRAM (plus paged KV); their CPU offload paths are not designed for 8 GB + 64 GB and do not support the hybrid GGUF path you need. Excluded.
- ExLlama family: dense-transformer focused, VRAM-resident; no path for a 23 GB model on 8 GB. Excluded.
- Ollama: wraps llama.cpp and hides exactly the knobs you need (`-ncmoe`, `--fit`, KV types, checkpoints, MTP). Excluded.
- KTransformers: real CPU/GPU heterogeneous engine and officially recommended by Qwen, but it targets large-VRAM workstations and Intel AMX servers; INSUFFICIENT EVIDENCE for an 8 GB laptop GPU. Not recommended.
- ik_llama.cpp fork: ANECDOTAL claims of much faster MoE offload (one poster reports 33 tok/s for 35B-A3B on an RTX 4060 laptop with 32 GB RAM, using an undisclosed patched fork). Unverifiable. Benchmark it in Phase 0 as an optional experiment only.

### 3.2 Daily configuration (model A)

```
llama-server \
  -m Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf \
  --alias daily \
  -ngl 99 --n-cpu-moe 40 \
  --fit on --fit-target 768 \
  -c 49152 -np 1 \
  -fa on -ctk q8_0 -ctv q8_0 \
  -b 2048 -ub 1024 \
  -t 10 --cpu-mask 0xFFF --cpu-strict 1 \
  --cache-ram 8192 --ctx-checkpoints 32 --checkpoint-every-nb 4096 \
  --spec-type draft-mtp --spec-draft-n-max 2 \
  --reasoning-budget 3072 \
  --jinja --temp 0.6 --top-p 0.95 --top-k 20 --min-p 0 --presence-penalty 0 \
  --no-context-shift --metrics --port 8080
```

Notes on each choice:
- `--n-cpu-moe 40` starts with all routed experts in RAM; then lower it step by step in Phase 0 until VRAM is nearly full (`--fit on` does this automatically on recent builds; keep both and compare).
- Context 48K: this is the server ceiling, not the working budget. The supervisor keeps the working prompt under 32K (see section 7). KV at 48K costs almost nothing on this architecture: only 10 of 40 layers hold KV, 2 KV heads x 256 dims x 2 (K and V) x 10 layers = 10,240 values per token = 20 KB/token at f16, about 10 KB/token at q8_0. 48K tokens is about 0.5 GB at q8_0 (ESTIMATE from the model card; confirm with the server's memory breakdown log). The real cost of long context here is prompt processing time and attention quality, not VRAM.
- `-ctk q8_0 -ctv q8_0`: your own runs confirmed q8_0 KV stable at 32K on the 27B. Keep f16 for the GDN state (llama.cpp handles that internally).
- Threads: 10 threads pinned to the P-core logical CPUs, matching what you already tuned.
- `--ctx-checkpoints` and `--checkpoint-every-nb`: these are the only prefix-reuse mechanism for hybrid models; the supervisor must build prompts so that checkpoints are hit (section 7). Watch the server log for "forcing full prompt re-processing"; count occurrences as a Phase 0 metric.
- `--reasoning-budget 3072` for tool-loop steps. Raise to 8192 for PLAN and REVIEW calls by passing per-request `chat_template_kwargs` or by running the call with a different budget. Qwen recommends 32K output for hard problems; on this GPU that is 20-40 minutes per call, so reserve it for the dense model and for explicit "think hard" requests.
- MTP draft: keep on, measure acceptance; if acceptance is under 60 percent on your prompts, turn it off (rejected drafts cost CPU expert reads).
- `-np 1`: one slot. Parallel slots would split the small VRAM and defeat checkpoints.

### 3.3 Review / hard configuration (model B)

Same server binary, separate profile, loaded only on demand by the supervisor (swap, never both):

```
llama-server -m Qwen3.8-27B-UD-Q4_K_XL.gguf --alias deep -ngl 99 --fit on --fit-target 768 \
  -c 65536 -np 1 -fa on -ctk q8_0 -ctv q8_0 -b 2048 -ub 1024 -t 10 --cpu-mask 0xFFF \
  --cache-ram 4096 --ctx-checkpoints 16 --reasoning-budget 8192 --jinja --temp 0.6 --top-p 0.95 --top-k 20
```

Use Q4_K_XL rather than your current Q8_0: Q8 gives you 2.9 tok/s and 28 GB; Q4 should give roughly 4-6 tok/s (ESTIMATE, bandwidth-proportional) at 16-17 GB. For a review call that reads a 20K diff-plus-context and writes 2K tokens of findings, that is about 90 s prefill plus 6-8 minutes generation. Acceptable once per task; not acceptable more often. Measure Q5/Q6 too; pick the largest quant that keeps review under 10 minutes.

Model swap: use llama-swap or a 40-line supervisor routine that stops one server and starts the other. Loading 23 GB from NVMe takes 10-30 s with page cache warm. Do not keep both resident; 23 + 17 GB plus KV and build memory would swap.

### 3.4 Expected footprint (ESTIMATE, confirm in Phase 0)

| Item | VRAM | RAM |
|---|---|---|
| Model A weights | 3-4 GB (attn, shared expert, embeddings, some experts) | 19-20 GB (routed experts) |
| KV + GDN state at 48K, q8_0 | 0.5-1.0 GB | 0 |
| Compute buffers, ub 1024 | 1.0-1.5 GB | 0.5 GB |
| Headroom target | 0.75 GB | |
| Total while model A runs | 6-7 GB | about 21 GB |
| Model B (when swapped in) | 6-7 GB | 11-12 GB |

Speed expectations for model A on this machine: INSUFFICIENT direct evidence for an RTX 4060 8 GB with 64 GB DDR5-5200 and mainline llama.cpp. Bandwidth reasoning gives a decode ceiling of 25-35 tok/s and a realistic 12-25 tok/s; prompt processing with expert weights streamed over PCIe per micro-batch should land in the 200-600 tok/s range with `-ub 1024`. These are estimates. The Phase 0 harness must record: tg at 0K, 16K, 32K depth; pp at 4K, 16K, 32K; cache hit rate; MTP acceptance.

---

## 4. FINAL ARCHITECTURE

```
                         USER (terminal)
                              |
                              v
+---------------------------------------------------------------------+
|  SUPERVISOR  ("le", Go binary, one process)                          |
|                                                                     |
|   Task Ledger (SQLite)   Phase Machine   Budgets   Failure Ctrl     |
|         |                    |               |         |            |
|         +---------+----------+-------+-------+---------+            |
|                   |                  |                              |
|          +--------v--------+  +------v-------+                      |
|          |  CONTEXT PACKER |  | TOOL FIREWALL|                      |
|          | frozen prefix + |  | schema, phase|                      |
|          | append-only log |  | path, cmd,   |                      |
|          +--------+--------+  | limits, log  |                      |
|                   |           +------+-------+                      |
|   +---------------+-----------+     |                               |
|   |  CODE INTELLIGENCE (deterministic)                              |
|   |  SCIP index (persistent xref graph)  <- scip-go/ts/rust-analyzer|
|   |  Live LSP client (gopls, rust-analyzer, tsserver-lsp)           |
|   |  Tree-sitter repo map (symbols, signatures, PageRank)           |
|   |  ripgrep lexical search                                         |
|   |  Impact engine (callers, impls, tests, schemas, migrations)     |
|   |  [optional] Qwen3-Embedding-0.6B on CPU + sqlite-vec            |
|   |  [optional] Qwen3-Reranker-0.6B on CPU                          |
|   +---------------+-------------------------------------------------+
|                   |                                                 |
|          +--------v---------+      +---------------------------+    |
|          | MODEL GATEWAY    |----->| llama-server (CUDA)       |    |
|          | OpenAI-compat    |      | profile daily: Qwen3.6-35B|    |
|          | thinking budget  |      | profile deep : Qwen3.8-27B|    |
|          | per phase        |      | (swap, never both)        |    |
|          +--------+---------+      +---------------------------+    |
|                   |                                                 |
|   Non-agentic phases (LOCALIZE, PLAN, REVIEW): single structured    |
|   calls made directly by the supervisor.                            |
|   Agentic phase (EDIT/REPAIR): OpenCode session inside the sandbox, |
|   with a minimal custom agent whose tools are proxied to the        |
|   firewall.                                                         |
|                   |                                                 |
|          +--------v---------+      +---------------------------+    |
|          | SANDBOX          |      | VERIFICATION              |    |
|          | bwrap: repo rw,  |----->| fmt, build, vet/tsc, test,|    |
|          | deps ro, no net, |      | lint, schema, migration   |    |
|          | secrets denied   |      | checks; repo-native cmds  |    |
|          +------------------+      +---------------------------+    |
|                                                                     |
|   MEMORY (.agent/ in repo, versioned)   TRACE (SQLite + JSONL)      |
+---------------------------------------------------------------------+
```

Data flow for one task: INTAKE -> LOCALIZE (supervisor calls: index queries, then one or two model calls) -> IMPACT (deterministic, no model) -> PLAN (one model call, structured) -> EDIT (OpenCode loop, firewall-gated) -> VERIFY (deterministic) -> REPAIR (bounded loop back into EDIT with failure fingerprint) -> REVIEW (fresh-context model call, optional deep model) -> FINALIZE (git commit on a task branch, memory update, trace close).

---

## 5. COMPONENT DECISIONS

| # | Component | Decision | Why |
|---|---|---|---|
| 1 | Coding shell: OpenCode | KEEP, but narrow its role | Use it only for the agentic EDIT/REPAIR phase, with a custom agent, minimal tool set, and the supervisor's tools exposed through a plugin. LOCALIZE/PLAN/REVIEW are single structured calls made by the supervisor without an agent loop (fewer tokens, no loop risk, deterministic prompt layout for caching). Aider is not a tool-calling agent and is not what you decided on; borrow its repo-map algorithm (Apache 2.0), not the tool. |
| 2 | Inference engine: llama.cpp | KEEP | Only engine that fits this hardware (section 3). |
| 3 | Serena + LSP | REPLACE (Serena) / KEEP (LSP) | Serena is a good MCP prototype but it is a Python process with 20-plus tools whose schemas eat context on a 3B-active model, and it does not give you a persistent cross-reference graph. Own the LSP clients in the supervisor (small tool surface) and add SCIP for the persistent index. |
| 4 | Repo map | KEEP, MODIFY | Tree-sitter map ranked by PageRank over the symbol reference graph (Aider's approach), but generated from the SCIP graph where available, capped at 2-4K tokens, and frozen into the cached prefix per task. |
| 5 | Hybrid retrieval | KEEP structure-first; MODIFY | Order: SCIP/LSP exact -> ripgrep lexical -> repo-map neighbours -> (optional) embeddings. Iterative, query-expanded. |
| 6 | Embedding model | OPTIONAL, phase-gated | Add only if Phase 6 A/B shows localization recall gain on natural-language tasks. If added: Qwen3-Embedding-0.6B GGUF on CPU. |
| 7 | Reranker | OPTIONAL, conditional | Only for natural-language localization queries with more than 30 candidates. If added: Qwen3-Reranker-0.6B GGUF on CPU through llama-server's rerank endpoint. |
| 8 | Context engine | MODIFY heavily | Frozen prefix + append-only log; re-pack only at phase boundaries; working budget 32K, ceiling 48K. |
| 9 | State machine | KEEP, simplify | 6 phases, custom Go FSM. No LangGraph, no PydanticAI. |
| 10 | Tool firewall | KEEP, MODIFY | Enforced in the supervisor and by an OS sandbox; the shell's permission config is not part of the trust boundary. |
| 11 | Impact analysis | KEEP, MODIFY | Computed from the SCIP graph plus Go/TS/Rust build graphs and a tests-to-symbols map; live LSP only to refresh dirty files. |
| 12 | Planning | KEEP | One structured plan call; plan declares the write allowlist; deviations require re-plan. |
| 13 | Verification | KEEP | Repo-native commands discovered, then confirmed once by the user and stored. |
| 14 | Failure controller | KEEP | Fingerprints, bounded retries, escalation ladder. |
| 15 | Diff review | KEEP, MODIFY | Same model, fresh context, is the default; deep model only when triggered. |
| 16 | Project memory | KEEP | Files + SQLite. REMOVE Mem0/Zep/Graphiti. |
| 17 | Repository isolation | KEEP | Repo id from root commit + remote + path; per-workspace state. |
| 18 | Traceability | KEEP | SQLite events table with JSONL export. No Langfuse/OpenTelemetry stack. |
| 19 | Prompt injection defense | KEEP, MODIFY | Provenance tagging, plan-scoped write allowlist, command allowlist, sandbox, no network, fresh-context review. |
| 20 | External docs | OPTIONAL, later | Local module cache first (Go module cache, node_modules, cargo registry, vendored docs). Web fetch is a separate tool, off by default, output tagged untrusted. |
| 21 | One model vs multi-agent | KEEP one model | Two roles (worker, fresh-context reviewer) with the same weights. No planner/coder/tester crews. |
| 23 | Compaction | KEEP, MODIFY | Structured state record, rebuilt from the ledger, not summarized from chat. |
| 24 | Cache | KEEP | Content-hash keyed, per workspace, invalidated by git status + file hashes. |
| 25 | Performance budget | KEEP | Section 15. |
| 26 | Evaluation | KEEP | Section 22, with a removal rule for weak components. |

---

## 6. RETRIEVAL ARCHITECTURE

Principle: a query is answered by the cheapest layer that can answer it deterministically. The model only sees the final pack.

### 6.1 Persistent indexes (built once, updated incrementally)

1. SCIP index per language package: `scip-go` for Go, `scip-typescript` for TS, `rust-analyzer scip` for Rust. SCIP is a protobuf file with every definition, reference, implementation, and symbol documentation, produced by the real compilers' front-ends. It is deterministic, cheap to store in SQLite, and does not need a running language server to query. The supervisor parses the protobuf into tables: `symbol`, `occurrence(symbol, file, range, role)`, `relationship(symbol, related, kind)`.
2. Tree-sitter symbol table for every file (Go, Rust, TS/Vue, SQL, YAML, protobuf, Avro schemas): fast, language-agnostic, used for the repo map, for files SCIP does not cover (schemas, migrations, configs), and as a fallback while SCIP is stale.
3. Reference graph: nodes are files and symbols; edges are SCIP references, imports, test-to-symbol edges (tests that mention a symbol name or call it), schema-to-code edges (Avro/protobuf message names found in code), migration-to-table edges (SQL table names found in Go/Rust queries), Kafka topic edges (topic constant strings shared between producer and consumer services). PageRank over this graph gives the "importance" score used by the repo map.
4. ripgrep is not an index; it is run live with a 200 ms budget per query and result caps.

### 6.2 Query router (deterministic, no model)

The supervisor classifies the task text and each retrieval request:
- Symbol-shaped query (identifier, CamelCase, dotted path, file path): SCIP lookup first; if not found, tree-sitter symbol table; if not found, ripgrep with word boundaries.
- Error-shaped query (stack trace, compiler diagnostic, test name): parse file:line and symbol from the trace, load exact ranges, then callers of those symbols.
- Concept-shaped query ("account limit business logic"): ripgrep over expanded terms (split words, snake and camel variants, synonyms from the project glossary in memory), then repo-map neighbourhood of hits, then optional embeddings.

### 6.3 Hierarchical localization for a task (Agentless-style, adapted)

1. Structure pass: model receives task text + repo map (2-4K tokens) + top ripgrep file hits (file paths only) and returns up to 10 candidate files with reasons. One call, no tools.
2. Symbol pass: for those files, model receives the tree-sitter skeleton (signatures only, no bodies) and returns candidate symbols. One call.
3. Expansion pass (deterministic): for each candidate symbol, SCIP adds definitions, direct callers, implementations, and associated tests. This is the localization set.
4. Confirmation pass: model receives the bodies of the localization set (capped at 12K tokens) and outputs the "confirmed symbols" list plus root-cause hypothesis. One call.

Total: three model calls with short outputs, each prompt sharing the same frozen prefix so only the tail is processed. This replaces the open-ended "agent explores the repo with read/grep tools" pattern, which is where small models burn context and get lost.

### 6.4 Ranking of candidates inside a pass (deterministic score)

score = 3.0 x exact_symbol_match + 2.0 x stack_trace_hit + 1.5 x (1 / (1 + call_graph_distance)) + 1.0 x lexical_bm25_norm + 0.8 x test_association + 0.5 x pagerank_norm + 0.5 x recently_edited_in_task + 0.3 x same_package_as_confirmed + (optional) 1.0 x embedding_cos_norm

Weights are constants in a config file and are tuned by the benchmark, not by intuition. Ties broken by smaller file size.

### 6.5 Why embeddings are optional

Evidence from the literature (training knowledge, verify before citing externally): Agentless (Xia et al., 2024) reached competitive SWE-bench Lite results with skeleton-based hierarchical localization and no embeddings; RepoCoder showed iterative retrieval beating single-shot retrieval; LocAgent and CodeRAG-style graph retrieval improve localization with graph traversal rather than dense vectors; Cursor and Claude Code moved away from index-heavy RAG toward agentic search over structure. Dense retrieval helps mostly for natural-language-to-concept queries in unfamiliar repos, which is a minority of daily tasks on your own codebase. The cost (a second model, an index of 100K+ chunks, invalidation per commit) is paid on every task. So: measure first.

---

## 7. CONTEXT ARCHITECTURE

### 7.1 The caching constraint that drives everything

For hybrid GDN models in llama.cpp, a prompt is cheap only if it is an exact prefix extension of the previous prompt on the same slot, and the server managed to keep a checkpoint at or before the divergence point. Any change earlier in the prompt (re-ranking retrieved snippets, editing the system prompt, dropping an old tool result) forces full re-processing. With prefill at a few hundred tok/s, a 30K prompt costs 1-2 minutes per call. Over 100 calls that is the whole day.

Therefore every model request is built as:

```
[PREFIX: frozen for the whole task]
  P0  operating policy (about 1.2K tokens, identical for all tasks)
  P1  repo card: name, languages, build/test commands, conventions (about 0.6K, from memory)
  P2  repo map for this task: PageRank-selected symbols, biased toward the localization set (2-4K)
  P3  task card: objective, acceptance criteria, constraints, plan (once plan exists) (about 1K)
[LOG: append-only during a phase]
  L1  evidence blocks in the order they were fetched (code ranges, SCIP results, test bodies)
  L2  tool calls and tool outputs (truncated deterministically at fetch time, never rewritten later)
  L3  model turns
[TAIL: rebuilt every call, small]
  T1  current phase instructions + allowed tools (about 0.3K)
  T2  budget note: tokens used, remaining, retry counter
```

Rules:
- Nothing in PREFIX or LOG is ever edited or reordered inside a phase. Truncation of tool output happens before it is appended, using fixed rules (head 60 lines + tail 40 lines for command output; ranges only for code).
- The tail is tiny so re-processing it is cheap.
- A phase boundary is the only place where the pack is rebuilt (new evidence order, compaction). At that moment one full prefill is paid deliberately, and P0-P3 are unchanged so the checkpointed prefix still hits.
- The server runs with `-np 1` and the supervisor never interleaves requests from two tasks.

### 7.2 Budget

Working budget per phase (server ceiling 48K):

| Segment | LOCALIZE | PLAN | EDIT step | REVIEW |
|---|---|---|---|---|
| PREFIX | 5-7K | 5-7K | 5-7K | 5-7K |
| LOG cap | 14K | 16K | 18K | 22K (diff + contracts) |
| Reserved output incl. thinking | 4K | 8K | 4K | 8K |
| Total ceiling | 26K | 32K | 30K | 38K |

Why 32K working rather than 64K or 128K: VRAM is not the limit on this architecture (section 3.2); prompt-processing time and attention quality are. At 200-600 tok/s prefill, a cold 64K prompt is 2-5 minutes, and a cold 128K prompt is 4-10 minutes. Your own measurement on the 27B showed generation falling from 2.9 to 1.79 tok/s between 0 and 27.5K depth; MoE decode will also slow with depth. Qwen's advice to keep 128K "to preserve thinking" is about not truncating long reasoning, which the budgeted thinking handles. 16K is too small for a multi-file edit step with tests. 32K working / 48K ceiling is the best trade for this GPU; re-evaluate after Phase 0 measurements at 16K, 32K, 48K, 64K.

### 7.3 Evidence selection per phase (what enters LOG)

- LOCALIZE: repo map, file skeletons, ranked candidate bodies (cap 12K).
- PLAN: confirmed symbol bodies, impact report (callers, impls, tests, schemas; names and signatures, not bodies), relevant contracts (interface definitions, Avro/proto schema for touched messages), the failing test if any.
- EDIT: the plan, the write allowlist, current bodies of files in the allowlist (ranges around target symbols, not whole files unless under 300 lines), the latest verification output (fingerprinted, truncated), and the diff so far.
- REVIEW: final diff, the plan, the impact report, contracts, verification summary. No exploration history.

### 7.4 Compaction (see section 12 and Component 23)

Compaction never summarizes chat. At a phase boundary the supervisor rebuilds the task card from the ledger (facts confirmed by tools, decisions, changes made, open failures) and starts a new LOG. Old LOG content remains in the trace database and can be re-fetched by symbol name if the model asks.

---

## 8. STATE MACHINE

Six phases. Every phase has an allowed tool set enforced by the firewall, an entry condition, an exit condition, a budget, and a failure edge.

```
INTAKE -> LOCALIZE -> IMPACT -> PLAN -> EDIT -> VERIFY -> REVIEW -> FINALIZE
                ^                       ^        |           |
                |                       +--------+ (repair,  |
                |                          bounded)          |
                +--------------------------------------------+ (re-localize on repeated failure or review reject)
```

| Phase | Who runs it | Tools allowed | Exit condition | Budget |
|---|---|---|---|---|
| INTAKE | supervisor, no model | none | task text parsed, repo identity resolved, indexes fresh or rebuilt, verification commands known | 1 index refresh |
| LOCALIZE | supervisor, 2-3 structured calls | read-only: repo_map, skeleton, read_range, search_symbol, search_text, scip_refs | confirmed_symbols non-empty and hypothesis written | 3 calls, 12K evidence |
| IMPACT | supervisor, no model | index only | impact report produced (may be empty for new-code tasks) | none |
| PLAN | supervisor, 1 structured call (deep model allowed) | read-only tools as in LOCALIZE | plan JSON validated: files, symbols, contracts, tests, write allowlist, risk flags | 1 call, 8K output |
| EDIT | OpenCode session in sandbox | read tools + edit_range, create_file (allowlist), run_verify (fmt/build/test presets only) | model declares done AND verification gates pass | max 40 tool calls, 3 verify runs per attempt |
| VERIFY | supervisor, no model | repo-native commands in sandbox | all required gates pass -> REVIEW; else fingerprint and route | per-gate timeouts |
| REVIEW | supervisor, 1 fresh-context call (daily or deep) | none (diff and contracts given inline) | verdict JSON: accept, or reject with findings | 1 call |
| FINALIZE | supervisor | git | commit on task branch, memory updated, trace closed | none |

Transitions from VERIFY failures (section 11): same fingerprint twice -> back to LOCALIZE with the failure as the query; new fingerprint -> EDIT (repair) with retry counter; environmental failure -> pause and ask the user; flaky test -> rerun once, then mark and continue.

Implementation: a Go `switch` on phase with a `Transition(event)` function and a persisted `phase` column in the task ledger. Every transition writes a trace event. Crash safety comes from the ledger: on restart the supervisor resumes at the recorded phase with the recorded LOG. No framework needed; LangGraph and PydanticAI add Python, a second process, and their own state model between you and your ledger.

Tools by phase are advertised to the model only for that phase (the tail T1 lists them), so the model's tool schemas stay small (6-8 tools, about 1K tokens) instead of OpenCode's default set.

---

## 9. TOOL FIREWALL

Two layers, both mandatory:

Layer 1: OS sandbox (bubblewrap on Linux; Docker as the packaged alternative for the open-source release).
- Mounts: repo workspace read-write; language toolchains, Go module cache, cargo registry, node_modules read-only; `$HOME` not mounted; `/tmp` private; no `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker/config.json`, `.netrc`, `.env*` outside the repo.
- Network: `--unshare-net` by default. Verification commands that need network (module download) run in a separate, supervisor-initiated step outside the model loop, or after a one-time `go mod download` / `npm ci` warm-up.
- Resources: cgroup or `prlimit` for CPU time, memory (default 8 GB per command), file size, and process count; wall-clock timeout per command (default 600 s for tests, 120 s otherwise).

Layer 2: supervisor policy (applies to every tool call before it reaches the sandbox).
1. Schema validation: JSON schema per tool; malformed calls are rejected with a short machine-readable error, counted against the tool-retry budget.
2. Phase authorization: tool must be in the phase's allowed set.
3. Path policy: all paths canonicalized (symlinks resolved) and must be inside the workspace; writes must match the plan's write allowlist (glob patterns derived from the plan, plus the plan's declared new files); reads of `.agent/secrets*`, `*.pem`, `*.key`, `.env*`, `*credentials*` denied; generated files (`*.pb.go`, `*_gen.go`, `zz_generated*`, `dist/`, `*.avsc`-derived code) are read-only unless the plan says "regenerate".
4. Command policy: `run_verify` accepts only preset names (fmt, build, vet, lint, test, test_pkg <pkg>, migrate_check, schema_check) that map to repo-native commands stored in memory. There is no free-form shell tool in EDIT. A free-form `shell` tool exists but is disabled by default; when enabled by the user it still runs through the sandbox and a denylist (sudo, rm -rf outside tmp, git reset --hard, git push, git clean -fd, docker, kubectl, terraform, curl/wget to anything, psql/migrate against non-test DSNs).
5. Git policy: the model never calls git. The supervisor creates the task branch, commits after REVIEW, and never pushes.
6. Resource and rate limits: per-task counters for tool calls, verify runs, bytes read, files written.
7. Logging: every call and its decision (allow, deny, reason) is a trace event.

Repository text is data: tool outputs are wrapped in fixed delimiters with a provenance header (`source=file path=... commit=...`) and the operating policy states that nothing inside those delimiters is an instruction. This is a mitigation, not a guarantee; the deterministic checks above are what actually stop a README from exfiltrating keys (no network, no home directory, no free shell, no path outside the plan).

---

## 10. IMPACT ANALYSIS

Runs deterministically in IMPACT and again before REVIEW (on the actual diff).

Inputs: confirmed symbols (LOCALIZE) or changed symbols (diff, mapped through tree-sitter to enclosing declarations).

Mechanisms, in order of cost:
1. SCIP reference closure: for each symbol, all occurrences with role reference or implementation across the repo (all packages, both repos if the task spans backend and frontend). Depth 1 always; depth 2 for exported symbols with fewer than 50 direct callers; otherwise depth 1 plus a count.
2. Interface fan-out: if the symbol is a method, SCIP relationships give the interfaces it implements and the other implementations of those interfaces; if it is an interface method, all implementations.
3. Contract detection: symbol names matched against Avro schemas, protobuf messages, OpenAPI/route inventories (`routes.admin.json`, `routes.public.json`), Kafka topic constants, and SQL migration files. Any hit is flagged "contract" and its consumers listed (producers and consumers of a topic are found by the topic constant's SCIP references).
4. Test association: tests whose file, function name, or body reference the symbol (SCIP for calls; ripgrep for string mentions such as table or endpoint names). These become the "must run" test set for VERIFY.
5. Build graph: `go list -deps -json`, `cargo metadata`, and the TS project references or workspace graph give the package-level dependents so VERIFY can pick `go build ./...` scope and `go test` scope.
6. Diff-time regression check (before REVIEW): recompute closure on changed symbols; any caller not in the plan's impact list is a finding ("unplanned caller affected: X"), and any exported signature change without all callers in the diff is a hard failure.

Not doing: program slicing, dynamic call-graph capture, CodePlan-style full dependency propagation across every change. CodePlan's lesson (plan as a graph of change obligations discovered incrementally) is kept in a cheap form: when the model edits a symbol whose signature changed, the supervisor automatically appends its callers to the plan's obligations list and the write allowlist, and the EDIT phase cannot declare done until each obligation is either edited or explicitly marked "no change needed" with a reason.

---

## 11. FAILURE RECOVERY

### 11.1 Fingerprint

```
fingerprint = sha256(
  tool_name,
  exit_class (0 ok, 1 fail, 2 timeout, 3 env),
  normalized_stderr,           # paths relative, line numbers kept, hex addresses, timestamps, durations, goroutine ids, temp dirs removed
  primary_diag_code,           # go vet check name, tsc TSxxxx, rustc Exxxx, eslint rule id, or ""
  primary_file,
  primary_symbol,              # enclosing declaration of primary_file:line via tree-sitter
  failed_test_ids (sorted)
)
```

Classification against the task's history:
- NEW: fingerprint not seen in this task.
- SAME: identical fingerprint after a repair attempt.
- REGRESSION: a test that passed at task start (baseline run in INTAKE) now fails.
- ENVIRONMENTAL: exit_class 3, or stderr matches env patterns (no space left, permission denied outside repo, network unreachable, module not found in cache, port in use).
- FLAKY: fails, then passes on an immediate rerun with no diff change; recorded in memory as a known flaky test with its id.

### 11.2 Bounded loop

Defaults: MAX_TOOL_RETRY 3 (malformed calls), MAX_PATCH_RETRY 3 (edit attempts per plan), MAX_VERIFY_LOOP 4, MAX_SAME_ERROR_RETRY 1.

Ladder:
1. NEW failure -> EDIT (repair) with the failure block appended; patch retry +1.
2. SAME failure once -> EDIT with an explicit instruction "the previous fix did not change the outcome; do not repeat it" plus the previous diff hunk.
3. SAME failure twice -> revert to the last verified state, go to LOCALIZE with the failure as the query, thinking budget raised, deep model allowed for PLAN. Plan retry +1.
4. Plan retry exhausted or REGRESSION count above 2 -> stop, restore branch to task start, report to user with the trace.
5. ENVIRONMENTAL -> pause, report, wait for user.
6. FLAKY -> rerun once, then continue with the test marked.

Every revert is a git operation by the supervisor (task branch, checkpoints as commits), never a model action.

---

## 12. PROJECT MEMORY

Location: `<repo>/.agent/` committed to the repo (so it travels with branches) plus `~/.le/repos/<repo_id>/` for machine-local caches.

Stored (durable, human-editable markdown with a small YAML header carrying `repo_id`, `updated_at`, `commit`, `source`):
- `project.md`: languages, module layout, build/test/lint commands (confirmed), how to run one package's tests, generated-code rules, known flaky tests.
- `architecture.md`: services, boundaries, Kafka topics and their owners, outbox pattern, database ownership. Written by the user or accepted from a model draft after review.
- `conventions.md`: error handling style, logging, naming, test structure, migration rules.
- `constraints.md`: things that must not change (public API stability, money math rules, idempotency keys).
- `decisions.md`: ADR-style entries, each with commit and date.
- `glossary.md`: domain terms to identifier mapping (for query expansion).
- `tasks/<task_id>.md`: objective, plan, status, final diff summary, review verdict.

Always recomputed from the repository, never trusted from memory: symbol locations, references, callers, implementations, imports, file contents, dependency versions, test lists. If a memory note mentions a symbol, the supervisor resolves it through SCIP at load time and marks the note stale if the symbol is gone.

SQLite (`~/.le/repos/<repo_id>/state.db`): ledger, trace, index tables, caches. No Mem0, Zep, or Graphiti: they add a service, an embedding dependency, and a second source of truth for code facts, which is exactly the failure mode you want to avoid.

---

## 13. REPOSITORY ISOLATION

```
repo_id      = sha256("v1" + root_commit_sha + "|" + normalized_remote_url)    # stable across clones and moves
                fallback when no commits: sha256("v1|noremote|" + realpath(git_root))
workspace_id = sha256(repo_id + "|" + realpath(worktree_root))                 # one per clone or worktree
scope_key    = workspace_id + "|" + branch + "|" + head_sha + "|" + dirty_hash  # for caches
```

- `normalized_remote_url`: origin URL with scheme, credentials, `.git` suffix, and case differences removed; forks with different remotes get different ids by design, and the user can alias two ids as "same project" in `~/.le/config`.
- Your layout (two repos under `~/src/example-co`, both on the same long-lived branch) gives two repo ids. A task that touches both is a "multi-repo task" with two workspaces in one ledger row; indexes stay separate; the supervisor never mixes evidence blocks without a `repo=` label.
- All index tables, caches, embeddings (if any), traces, and task rows carry `workspace_id`. Queries always filter on it. There is no global index.
- Memory files live in the repo, so they are branch-scoped by git itself.

---

## 14. SECURITY MODEL

Threats: indirect prompt injection from repo text or docs; model misuse of shell; secret exfiltration; destructive git or database actions; unrelated directory access.

Defenses, layered:
1. No free shell in the loop; verification is preset-only (section 9).
2. OS sandbox with no network and no home directory; only the workspace is writable.
3. Plan-scoped write allowlist: an injected instruction cannot make the model write a file the plan did not declare; adding a file requires a re-plan, which is a structured call the user can be asked to confirm for sensitive paths (`migrations/`, `*.avsc`, `Dockerfile`, CI config).
4. Provenance tagging of all tool output and a policy prefix that names the tag as data. Known to be weak alone (this is why 1-3 exist).
5. Fresh-context REVIEW that sees only the diff and the plan: an injected instruction that made it into the edit shows up as an unplanned change.
6. Secrets: denylist patterns on read; a pre-commit scan of the diff for key-shaped strings; the supervisor refuses to commit if found.
7. Git: model never runs git; no push; task branches only.
8. External documentation (Component 20): a separate `fetch_doc` tool, disabled by default, allowed only for a domain allowlist (pkg.go.dev, docs.rs, npmjs.com, official project docs), output truncated and tagged, never cached into memory files without user review.

Honest limit: prompt injection is not solved. The literature (CaMeL from Google DeepMind 2025, the "lethal trifecta" framing, spotlighting and delimiter defenses) agrees that model-side defenses fail under adaptive attack; capability containment (what the agent can do) is what holds. Steps 1-3 and 7 are capability containment. Keep them strict even when it is inconvenient.

---

## 15. PERFORMANCE BUDGET (ESTIMATE unless marked)

| Component | VRAM | RAM | CPU | Disk |
|---|---|---|---|---|
| llama-server, model A running | 6-7 GB | 21-23 GB | 10 threads during decode/prefill | 23 GB weights |
| llama-server, model B (swapped) | 6-7 GB | 11-13 GB | same | 17 GB (keep your Q8 too if disk allows) |
| gopls (25 Go services, one module) | 0 | 1.5-4 GB (VERIFIED range from general gopls behaviour on large modules; measure yours) | bursts | cache in `~/.cache/go-build` |
| rust-analyzer (Rust services) | 0 | 1-3 GB | bursts | target dir large |
| typescript-language-server (5 Nuxt apps) | 0 | 1-2 GB per project opened; open one at a time | bursts | |
| SCIP indexes (all languages) | 0 | loaded into SQLite, not RAM | rebuild minutes, incremental seconds | 100-500 MB |
| Tree-sitter map + graph | 0 | under 300 MB | seconds | under 100 MB |
| Supervisor + SQLite | 0 | under 200 MB | low | traces grow about 1-5 MB per task |
| Optional Qwen3-Embedding-0.6B (CPU, Q8) | 0 | 0.7 GB | 1-2 threads; full repo of 1M tokens takes minutes on CPU | vectors 1-2 GB for a large repo |
| Optional Qwen3-Reranker-0.6B (CPU) | 0 | 0.7 GB | 0.1-0.5 s per candidate on 4 threads | 0 |
| Builds and tests (Go, Rust, Vitest) | 0 | 4-12 GB peak | all cores; pauses decode | |

Rule: never run LSP indexing, SCIP rebuild, or the test suite while the model is generating; the supervisor serializes GPU-heavy and CPU-heavy work. Total RAM at peak (model A + gopls + rust-analyzer + tests) is about 40 GB, inside 61 GiB with margin. Model B plus a full Rust build is the tightest case; run REVIEW before or after builds, not during.

---

## 16. IMPLEMENTATION STACK (final choices)

- Runtime: llama.cpp master, CUDA build, `llama-server`; llama-swap for profile switching (or the supervisor's own restart routine).
- Models: Qwen3.6-35B-A3B UD-Q4_K_XL with MTP draft (daily); Qwen3.8-27B UD-Q4_K_XL (deep). Optional: Qwen3-Embedding-0.6B Q8 GGUF, Qwen3-Reranker-0.6B Q8 GGUF, both served by a second llama-server on CPU only when enabled.
- Shell: OpenCode, pinned version, with a custom agent (`le-editor`) and a plugin that registers the supervisor's tools and forwards `tool.execute.before` to the firewall. Given the documented hook-bypass bugs, the plugin is a convenience; the sandbox and supervisor checks are the boundary.
- Supervisor: Go. Libraries: `modernc.org/sqlite` (pure Go SQLite), `github.com/smacker/go-tree-sitter` with Go, Rust, TypeScript, Vue (or HTML+TS), SQL, YAML, proto grammars, `google.golang.org/protobuf` with the SCIP proto, a small LSP client (write it; ~600 lines for initialize, definition, references, implementation, callHierarchy, diagnostics), `os/exec` with bubblewrap for the sandbox.
- Indexers: scip-go, scip-typescript, rust-analyzer (`rust-analyzer scip`), scip CLI for validation.
- Language servers for live queries: gopls, rust-analyzer, typescript-language-server (or vtsls), Vue language server for `.vue` files.
- Search: ripgrep.
- Verification presets: gofmt/gofumpt, go build, go vet, staticcheck, go test with `-run` scoping, cargo fmt/check/clippy/test, tsc, eslint, vitest, plus `migrate` dry-run and Avro schema compatibility check scripts you already have.
- Benchmark harness: Go, driving the supervisor in headless mode against task fixtures (section 22).

---

## 17. WHAT WE SHOULD BUILD OURSELVES

- The supervisor: phase machine, ledger, budgets, failure controller (about 2-3K lines of Go). Nothing off the shelf matches "state gates tools" without dragging a framework.
- The context packer with prefix freezing and phase-boundary repacking. No framework knows about the hybrid-model caching rule.
- The SCIP-to-SQLite loader and the reference graph with PageRank (about 800 lines). Sourcegraph's server is far too heavy; the SCIP format itself is small and stable.
- The thin LSP client and the tool surface (8 tools).
- The impact engine (contracts, topics, migrations, tests): this is specific to your stack and is the part with the most leverage.
- The tool firewall and sandbox launcher.
- The failure fingerprinter and stderr normalizer per toolchain.
- The benchmark harness and task fixtures from your own repos.

Borrow, do not rewrite: Aider's repo-map ranking idea, mini-SWE-agent's minimal tool surface as a reference, OpenCode's editor UI and edit tools.

---

## 18. WHAT WE SHOULD NOT INSTALL

- LangGraph, PydanticAI, CrewAI, AutoGen, Semantic Kernel: second runtime, opaque state, no gain over a Go switch statement.
- Mem0, Zep, Graphiti, or any "memory layer": they store code facts that go stale and require embeddings and a service.
- Vector databases (Qdrant, Milvus, Chroma, Weaviate): if embeddings are ever enabled, sqlite-vec in the existing database is enough for a few hundred thousand vectors.
- Ollama, LM Studio, Jan: hide the offload knobs you need.
- vLLM, SGLang, KTransformers, ExLlama: not fit for 8 GB + 64 GB hybrid GGUF workloads.
- Sourcegraph server, Zoekt, OpenGrok: heavy services; SCIP files plus SQLite give you the same facts.
- Serena as a permanent dependency: prototype tool, not the structural layer.
- Langfuse, OpenTelemetry collectors, Phoenix: a SQLite table is your trace store.
- Multi-agent orchestration of any kind on one GPU.
- "Semantic caches" and prompt-compression libraries (LLMLingua and friends): they rewrite prompts and destroy prefix caching.

---

## 19. DIRECTORY STRUCTURE

```
~/.le/                                  # machine-local
  config.toml                           # model profiles, sandbox settings, repo aliases
  bin/                                  # llama-server, scip-*, bwrap wrappers
  models/                               # GGUF files
  repos/<repo_id>/
    workspaces/<workspace_id>/
      state.db                          # ledger, trace, index, caches (SQLite, WAL)
      scip/<package>.scip               # raw SCIP outputs
      cache/                            # repo map snapshots, skeletons keyed by content hash
      logs/                             # llama-server and command logs per task

<repo>/.agent/                          # versioned with the repo
  project.md  architecture.md  conventions.md  constraints.md  decisions.md  glossary.md
  tasks/<task_id>.md
  verify.toml                           # confirmed verification presets
  policy.toml                           # optional per-repo firewall overrides (stricter only)

le/                                     # the open-source project
  cmd/le/                               # CLI: le task "...", le index, le bench, le trace <task>
  internal/supervisor/  (phases, ledger, budgets, failure)
  internal/context/     (packer, prefix freeze, compaction)
  internal/index/       (treesitter, scip loader, graph, pagerank, ripgrep)
  internal/lsp/         (client, tool adapters)
  internal/impact/
  internal/firewall/    (policy, sandbox)
  internal/verify/      (presets, discovery, fingerprints)
  internal/gateway/     (llama-server client, profiles, swap)
  internal/opencode/    (plugin bridge, agent config generation)
  internal/memory/
  internal/trace/
  bench/                (harness, task fixtures, report generator)
  docker/               (single-container distribution)
```

---

## 20. DATA SCHEMAS (SQLite DDL, abbreviated)

```sql
CREATE TABLE repo (
  repo_id TEXT PRIMARY KEY, root_commit TEXT, remote_norm TEXT, first_seen TEXT);
CREATE TABLE workspace (
  workspace_id TEXT PRIMARY KEY, repo_id TEXT REFERENCES repo, path TEXT, kind TEXT CHECK(kind IN ('clone','worktree')));

CREATE TABLE task (
  task_id TEXT PRIMARY KEY, workspace_id TEXT, created_at TEXT, objective TEXT,
  acceptance TEXT, phase TEXT, status TEXT CHECK(status IN ('running','blocked','done','failed','aborted')),
  branch TEXT, base_sha TEXT, head_sha TEXT,
  plan_json TEXT, write_allowlist_json TEXT, obligations_json TEXT,
  budgets_json TEXT, counters_json TEXT, model_profile TEXT, updated_at TEXT);

CREATE TABLE trace_event (
  id INTEGER PRIMARY KEY, task_id TEXT, ts TEXT, phase TEXT, seq INTEGER,
  kind TEXT CHECK(kind IN ('phase','query','retrieval','evidence','model_call','tool_call','tool_result',
                           'firewall','edit','verify','failure','review','memory','git','note')),
  payload_json TEXT, prompt_tokens INTEGER, completion_tokens INTEGER, cache_hit_tokens INTEGER, duration_ms INTEGER);
CREATE INDEX trace_task ON trace_event(task_id, seq);

CREATE TABLE retrieval_result (
  id INTEGER PRIMARY KEY, task_id TEXT, phase TEXT, query TEXT, query_kind TEXT,
  candidate_ref TEXT,   -- 'file:path' or 'sym:<scip symbol>'
  layer TEXT CHECK(layer IN ('scip','lsp','treesitter','ripgrep','graph','embedding','rerank')),
  score REAL, score_terms_json TEXT, selected INTEGER, rank INTEGER);

CREATE TABLE failure (
  fingerprint TEXT, task_id TEXT, attempt INTEGER, ts TEXT,
  tool TEXT, exit_class INTEGER, diag_code TEXT, primary_file TEXT, primary_symbol TEXT,
  failed_tests_json TEXT, class TEXT CHECK(class IN ('new','same','regression','environmental','flaky')),
  stderr_head TEXT, PRIMARY KEY(fingerprint, task_id, attempt));

CREATE TABLE symbol (symbol TEXT PRIMARY KEY, workspace_id TEXT, kind TEXT, display TEXT, file TEXT, line INTEGER, signature TEXT, doc TEXT, pagerank REAL, content_hash TEXT);
CREATE TABLE occurrence (symbol TEXT, file TEXT, line INTEGER, col INTEGER, role INTEGER);   -- role bitmask per SCIP
CREATE TABLE relationship (symbol TEXT, related TEXT, kind TEXT);                            -- implements, references, type_definition
CREATE TABLE file_state (workspace_id TEXT, path TEXT, content_hash TEXT, indexed_at TEXT, scip_fresh INTEGER, PRIMARY KEY(workspace_id, path));
CREATE TABLE test_map (test_id TEXT, symbol TEXT, file TEXT, kind TEXT);                     -- kind: call, mention
CREATE TABLE contract (name TEXT, kind TEXT, file TEXT, symbols_json TEXT);                  -- avro, proto, route, topic, table

CREATE TABLE memory_note (
  note_id TEXT PRIMARY KEY, workspace_id TEXT, file TEXT, section TEXT, commit_sha TEXT, ts TEXT,
  source TEXT CHECK(source IN ('user','model_accepted','tool')), stale INTEGER DEFAULT 0);
```

Task state JSON (`task.plan_json`):

```json
{ "root_cause": "...", "files": ["backend/orders-service/checks/order_check.go"],
  "symbols": ["scip-go gomod backend orders-service/checks OrderChecker#Check()."],
  "contracts": [{"kind":"avro","name":"OrderCheckEvaluated"}],
  "tests": ["TestOrderCheck_Check_*", "TestCheckout_RejectsOverLimit"],
  "risks": {"compatibility":"medium","migration":"none","rollback":"revert commit"},
  "write_allowlist": ["backend/orders-service/checks/*.go", "backend/orders-service/checks/testdata/**"],
  "obligations": [{"symbol":"...", "reason":"signature change", "status":"open"}] }
```

Compaction record (task card rebuilt at each phase boundary):

```json
{ "objective": "...", "confirmed_facts": [{"fact":"...", "evidence":"scip:...", "commit":"..."}],
  "files_examined": ["..."], "relevant_symbols": ["..."], "hypothesis": "...",
  "changes_made": [{"file":"...","symbol":"...","summary":"..."}],
  "test_status": {"ran":["..."],"passed":12,"failed":1}, "open_failures": ["<fingerprint>"],
  "decisions": ["..."], "next_action": "..." }
```

---

## 21. END-TO-END EXAMPLE

Task: "In orders-service, change `OrderChecker.Check` so that the account limit is evaluated against committed plus pending totals instead of committed only, without breaking callers."

1. INTAKE. Supervisor resolves repo id for `backend/`, checks `git status` (clean), refreshes SCIP for changed packages (none), loads `verify.toml` (gofmt, go vet, staticcheck, `go test ./...` with package scoping, avro compatibility script). Baseline: `go test ./orders-service/...` result recorded (all pass). Task branch `le/task-2026-09-17-a1` created.
2. LOCALIZE. Query router sees symbol-shaped tokens (`OrderChecker.Check`, `orders-service`) and concept tokens ("the account limit", "pending totals"). SCIP finds the symbol; ripgrep with expansion (`accountLimit`, `account_limit`, `AccountLimit`, `pending`, `PendingTotal`) returns 14 files. Call 1 (repo map + file list): model picks 6 files. Call 2 (skeletons): model picks `Check`, `limitExceeded`, `totalsSnapshot`, `AccountState`. Expansion adds callers: `checkout.Handler.Place`, `batchworker.evaluateBatch`, an interface `Checker` implemented by `OrderChecker` and `noopChecker` (tests), and tests `TestOrderCheck_Check_*`. Call 3 (bodies, 9K tokens): model confirms root cause is `totalsSnapshot.Committed` used alone in `limitExceeded`, and that `AccountState` already exposes `Pending`.
3. IMPACT (no model). Closure: `Check` has 2 callers; `Checker` has 2 implementations; `limitExceeded` is private with 1 caller; contract check: the Avro message `OrderCheckEvaluated` includes a field `limit_basis` (enum committed|total) -> flagged as contract; Kafka topic `orders.checks.evaluated` has one consumer in `reporting-service` -> listed. Tests: 7 unit tests plus 1 integration test tagged `#[ignore]`-style skip in CI. Build scope: `./orders-service/...` and `./reporting-service/...` (consumer reads the field).
4. PLAN (one call, daily model, budget 8K). Plan: change `limitExceeded` to use `Committed + Pending`; set `limit_basis` to `total` in the emitted event; keep `Check` signature; add a table-driven test case; no migration; risk: analytics consumer treats basis enum -> verify it handles `total` (it does, found by SCIP in `reporting/consumer/checks.go`). Write allowlist: `orders-service/checks/*.go`, `orders-service/checks/*_test.go`. Obligations: none open (no signature change). Plan validated by the supervisor (all files exist, all symbols resolve).
5. EDIT (OpenCode session, custom agent, tools: read_range, search_symbol, edit_range, run_verify). Prompt prefix frozen (policy, repo card, task repo map, task card with plan). Model edits `order_check.go` (2 hunks) and `order_check_test.go` (1 hunk) in 4 tool calls; calls `run_verify fmt` and `run_verify test_pkg orders-service/checks`. Firewall: allowed (paths in allowlist, presets only). Test fails: expected event basis mismatch in `TestOrderCheck_Check_EmitsEvent`. Fingerprint NEW -> repair within EDIT: model updates the test expectation (the supervisor flags "test changed" for REVIEW). Second verify passes. Model declares done.
6. VERIFY (supervisor). Full preset run: gofmt ok, go vet ok, staticcheck ok, `go test ./orders-service/... ./reporting-service/...` pass (48 s), avro compatibility script ok (no schema change). Diff-time impact recheck: changed symbols `limitExceeded`, `emitCheckEvent`, test; callers unchanged; no unplanned files. Gate passes.
7. REVIEW (fresh context, daily model; deep model not triggered because diff is under 200 lines, no contract schema changed, no test deleted). Input: diff, plan, impact report, `Checker` interface, the Avro schema fragment, verification summary. Findings: "test expectation changed from realized to total: consistent with plan" (accepted), "consider logging basis" (note, not blocking). Verdict: accept.
8. FINALIZE. Commit on task branch with message generated from the plan; `tasks/<id>.md` written; `decisions.md` gets one line ("the account limit uses committed plus pending totals since <sha>"); trace closed. Report to user: files changed, tests run, review notes, and the trace id. The user decides whether to merge.

Trace answers "why did the agent modify order_check_test.go?": event chain shows the failing test fingerprint, the repair decision, and the review acceptance.

---

## 22. BENCHMARK PLAN

Goal: measure each component's marginal value on your own repositories, with the same model, and remove components that do not pay.

Task set: 30-50 tasks mined from your git history (backend and frontend repos). For each: the commit before a real fix or feature, the original issue text or a rewritten one-paragraph task, the fixing commit's tests as hidden tests, the verification presets. Add 5 synthetic regression tasks with known blast radius (rename a field used by a Kafka consumer, change an interface). Split 10 tasks as a tuning set (used to set ranking weights) and the rest as the held-out set.

Harness (`le bench`): checks out the base commit into a fresh worktree, runs the supervisor headless with a configuration flag set, captures the ledger and trace, applies hidden tests, runs full verification, and records metrics. Each configuration runs each task 3 times (temperature 0.6) to estimate variance; report mean and pass@1 with a binomial confidence interval.

Configurations (each adds one thing; if a later letter is on, all earlier letters are on):
- A: OpenCode + model, default tools, no supervisor.
- B: + supervisor phases and firewall (no index, ripgrep only).
- C: + tree-sitter repo map.
- D: + SCIP/LSP structured retrieval and hierarchical localization.
- E: + impact analysis and obligations.
- F: + failure controller (fingerprints, ladder).
- G: + fresh-context review (daily model).
- H: + deep-model review.
- I: + embeddings.
- J: + reranker.
- K: + project memory.

Metrics per run: task success (hidden tests pass and full verification passes), build pass, test pass, regression count (baseline tests broken), files modified vs files in the fixing commit (precision, recall), retries, tool calls, prompt tokens, cache-hit tokens, prefill seconds, generation seconds, wall time, localization precision and recall at file and symbol level against the fixing commit, peak RAM and VRAM, number of "forcing full prompt re-processing" events.

Removal rule: a component stays only if it improves task success or regression rate on the held-out set by more than the run-to-run noise, or reduces wall time by more than 20 percent without hurting success. Components that fail the rule are turned off in the default profile, not deleted, and the result is written into `bench/RESULTS.md` with the date and model version.

Blinded comparison against a frontier model on 10 tasks stays in the first-year scope as you decided; it is not part of the component ladder.

---

## 23. IMPLEMENTATION ORDER

0. Phase 0 (1 week): download models; build llama.cpp master; run the inference matrix (models A, B, and optionally C and D) with `llama-bench` and a real OpenCode session; record tg/pp at 0K, 16K, 32K, 48K; cache-hit behaviour; MTP acceptance. Decide daily and deep profiles. Depends on nothing. Everything else depends on this.
1. Repo identity, SQLite ledger, trace store, `le` CLI skeleton, headless mode. Depends on 0 only for model config.
2. Sandbox launcher (bwrap), verification preset discovery and `verify.toml`, baseline test run, failure fingerprinter. Depends on 1.
3. Index: tree-sitter symbol tables, SCIP loader, reference graph, PageRank, repo map, ripgrep wrapper, content-hash invalidation. Depends on 1.
4. Context packer with frozen prefix and phase-boundary repack; model gateway with per-phase budgets and profile swap. Depends on 0, 1.
5. Supervisor phases LOCALIZE, PLAN, VERIFY, REVIEW as direct calls (no OpenCode yet); a minimal built-in edit tool so end-to-end runs work. Depends on 2, 3, 4.
6. Benchmark harness and the first 15 tasks; run A (OpenCode alone) and the current build. Depends on 5.
7. OpenCode integration for EDIT: custom agent, plugin bridge, tool proxying, firewall path and preset checks. Depends on 5. Compare against the built-in editor in the harness; keep whichever wins.
8. Impact engine: SCIP closure, interfaces, contracts (Avro, routes, topics, migrations), test map, obligations. Depends on 3, 5.
9. Failure controller ladder, reverts, re-localize; compaction records. Depends on 5, 8.
10. Memory files, stale-note detection, decision writing. Depends on 5.
11. Optional retrieval: embeddings and reranker behind flags; A/B I and J. Depends on 6.
12. External docs tool (local module cache first), domain allowlist. Depends on 2.
13. Packaging: Docker single-container distribution, docs, RESULTS.md. Depends on all.

---

## 24. RISKS

1. Prompt-cache behaviour on hybrid models regresses with a llama.cpp update, turning every call into a full prefill. Mitigation: pin the llama.cpp commit per profile; the harness counts full re-processing events; upgrade only when the count is unchanged.
2. Decode speed on this laptop lands at the low end (10-12 tok/s) and long EDIT loops become tedious. Mitigation: MTP draft, shorter thinking budgets, fewer tool calls through hierarchical localization; if still too slow, UD-IQ4_XS and fewer experts in RAM.
3. The 3B-active model is not reliable enough at multi-file edits even with the scaffold. Mitigation: the plan-obligation mechanism and the deep model for PLAN on hard tasks; measure with the harness before concluding; Qwen3-Coder-Next as fallback candidate.
4. OpenCode's plugin and permission surface keeps changing and breaks the bridge. Mitigation: the built-in editor from step 5 remains a supported fallback; OpenCode is pinned.
5. SCIP indexers lag on toolchain versions (Go 1.2x, TS 6, rust-analyzer changes). Mitigation: tree-sitter fallback for symbols; live LSP for references when SCIP is stale.
6. Test suites too slow for the loop (your Go baseline is 8,361 tests). Mitigation: package-scoped test presets from the build graph; full run only in VERIFY at the end.
7. Memory pressure when Rust builds coincide with the model. Mitigation: serialize; `cargo` job limits; swapfile as last resort.
8. Over-building before measuring. Mitigation: the removal rule and the ladder order above.

---

## 25. FINAL RECOMMENDATION

On this exact machine I would build the system in section 4 with these fixed choices: llama.cpp master pinned per profile; Qwen3.6-35B-A3B UD-Q4_K_XL with MTP as the only model in the loop; Qwen3.8-27B UD-Q4_K_XL swapped in for PLAN on hard tasks and for REVIEW when the diff touches contracts, migrations, or more than 200 lines; a Go supervisor that owns phases, budgets, the frozen-prefix context packer, the firewall, verification, fingerprints, memory, and traces; SCIP plus tree-sitter plus ripgrep as the entire retrieval stack until the benchmark proves embeddings earn a place; OpenCode used only for the agentic edit phase inside a bubblewrap sandbox; six phases, not fourteen; one model, two roles; files and SQLite for memory and traces.

The two things to get right first, because everything else depends on them: measure the real inference numbers in Phase 0, and design every prompt as a frozen prefix with an append-only tail so the hybrid model's cache is hit. If those two hold, the rest of the architecture is ordinary engineering that you already do for production services.

---

## Sources

Official and primary:
- Qwen3.6-35B-A3B model card (architecture, context, benchmarks, sampling): https://huggingface.co/Qwen/Qwen3.6-35B-A3B and the Unsloth mirror with GGUF sizes and MTP flags: https://huggingface.co/unsloth/Qwen3.6-35B-A3B-MTP-GGUF
- Qwen3.8-27B model card: https://huggingface.co/Qwen/Qwen3.8-27B
- Qwen3.8-Flash-Next card, blog, and repo: https://huggingface.co/Qwen/Qwen3.8-Flash-Next , https://qwen.ai/blog?id=qwen3.8-flash-next , https://github.com/QwenLM/Qwen3.8-Flash-Next
- Unsloth Flash-Next run guide: https://unsloth.ai/docs/models/qwen3.8-next
- GLM-5.3-Flash card (size, excluded): https://huggingface.co/zai-org/GLM-5.3-Flash
- llama.cpp hybrid-model cache issues: https://github.com/ggml-org/llama.cpp/issues/19794 , /issues/19858 , /issues/20225 , /issues/22384 , /issues/22746 , /issues/24055 , /issues/24587 , /issues/18497
- OpenCode permission and hook bypass reports: https://github.com/anomalyco/opencode/issues/5894 , /issues/6396 , /issues/21075
- Serena repo and roadmap: https://github.com/oraios/serena
- Qwen3-Embedding-0.6B (Apache 2.0, 32K, code retrieval): https://huggingface.co/Qwen/Qwen3-Embedding-0.6B

Community (anecdotal):
- Flash-Next speed thread with 64 GB configurations: https://huggingface.co/unsloth/Qwen3.8-Flash-Next-GGUF/discussions/3
- Qwen3.6-35B-A3B on RTX 3090 with OpenCode: https://huggingface.co/Qwen/Qwen3.6-35B-A3B/discussions/37
- Qwen3.6-35B-A3B on RTX 4060 laptop, undisclosed fork (unverifiable): https://huggingface.co/Qwen/Qwen3.6-35B-A3B/discussions/58
- Hacker News on Qwen3.6-35B in OpenCode as a workhorse: https://news.ycombinator.com/item?id=49150809

Research referenced from prior knowledge (verify wording before external citation): Agentless (Xia et al. 2024), SWE-agent (Yang et al. 2024), AutoCodeRover (Zhang et al. 2024), CodePlan (Bairi et al. 2023), RepoCoder (Zhang et al. 2023), LocAgent / CodeRAG graph retrieval (2025), CaMeL (Debenedetti et al. 2025), "lethal trifecta" (Willison 2025).

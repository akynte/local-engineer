# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Released sections are generated from Conventional Commits at release time.

## [Unreleased]

### Added

- **The §6.1 allowlisting egress proxy, and the `deps` and `docs` provisioning
  lanes it exists for.** A task sandbox has no egress at all — verification runs
  with `GOPROXY=off` — which left a change that legitimately needs a new
  dependency with no supported path. `le deps sync` and `le docs fetch` are that
  path: a confined process whose only route out is a forward proxy that will
  connect to an allowlisted host and refuse everything else.
  - One listener per lane, so the lane comes from which socket the client
    reached rather than from the request, where a client could choose it.
  - Hosts are exact or carry one leading wildcard label; `*` is refused, a
    wildcard does not cover its apex, and every rule needs a written `why`.
  - Ports 80 and 443 only, and deliberately not configurable: widening that
    turns a host allowlist into a general tunnel.
  - Every decision is journalled. A proxy that refuses silently is
    indistinguishable from a network fault, and the operator debugs DNS.
  - **A task is never granted the proxy port**, and there is no flag that would
    be — the only function that builds a sandbox spec containing it takes a
    `proxy.Lane`, which a task runner cannot construct. The separation is in the
    type, not in a convention.
  - Off by default, and refused alongside `offline` rather than one silently
    winning.
- **Repository-declared runtime and generation checks** (`.le/verify.yaml`),
  closing the last two §10.1 rows that were adopted in a table and built
  nowhere. Neither can be a built-in: how to bring up a database, and which
  generator writes which files, are facts about a repository.
  - `integration:` is up-then-test-then-down with **teardown guaranteed**,
    including after a failing `up` and after a timeout — the moment you most
    need teardown is when something is still running. Declared ports are the
    only ones the sandbox grants.
  - `generate:` runs the generator, compares its declared outputs, and
    **restores the worktree exactly as it was found**. That restore is what
    makes it safe inside verification: a generator that left its output behind
    would make every earlier result evidence about a candidate that no longer
    exists (§7.2), and would put generator output in the model's diff.
  - `check:` runs a repository's own analyzers, which is how `gosec`,
    `gitleaks`, `squawk`, `buf` and `oasdiff` become reachable.
  - A failing `up` or a missing generator is an **error**, not a failure: a
    database that would not start says nothing about the change.
- **`le tui`** (§4.1): a repainting status view for `docker exec -it … le tui`
  showing children, sandbox layers, live tasks and waiting gates. Read-only on
  purpose — approving from a status screen would be a way to approve without
  reading, which is the failure the gates exist to prevent. Degrades to plain
  appended blocks without a TTY, so it can be piped.
- **The optional cross-workspace telemetry aggregate** (§2.2): `le telemetry
  aggregate`. Workspace ids, metric names, a UTC day and two numbers — the row
  shape has nowhere to put content, and a test asserts the column set because
  adding a column is exactly how "counters, never content" would stop holding.
  Built on demand rather than continuously, so nothing accumulates across your
  projects while you are not looking.
- **`scripts/install-bare-metal.sh`** (§13) and a how-to page for it. It reports
  every prerequisite at once, installs the sidecar with the same probe the image
  build runs, and says plainly what a host install gives up: DR-3 layer 1, the
  boundary that bounds the whole system to the repositories you mounted.
- **The remaining §4.2 image tools**: `gosec`, `gitleaks`, `osv-scanner`,
  `buf`, `oasdiff` (pinned `go install`) and `squawk` (pinned npm). Playwright
  and trivy stay out, each with a reason recorded rather than left as an
  absence.
- **The docs site** (§1.1, §15): `mkdocs.yml` over the existing Diátaxis
  layout, built with `--strict` in CI so a broken internal link or an
  unlisted page fails the build. A Go test keeps the nav and `docs/` in step.
  Six cross-root links that worked on GitHub but not on a site are now absolute
  URLs, which work in both.
- **The 60-second demo** (§15): `docs/demo.cast`, asciicast v2, **generated** by
  `scripts/record-demo.sh` from real command output inside the image. A
  hand-written demo is a screenshot of a system that may no longer exist.

- **The TypeScript sidecar now ships in the `cpu` and `cuda` images.** It was
  built, tested and wired into `le index`, and then never copied into the
  image — which installed Node "for the TypeScript sidecars" and no sidecar. A
  missing sidecar is an ordinary condition the analyzer survives by falling
  back to the lexical reading, so the only symptom was that every Docker
  install silently had no TypeScript call graph. The image build now installs
  it from the committed lockfile, sets `LE_TYPESCRIPT_SIDECAR_DIR`, and fails
  the build if a probe file produces no `calls` edge; CI indexes a TypeScript
  fixture inside the finished image and asserts the same thing, because a
  `calls` edge is one the lexical reader emits by design never.
- **golangci-lint and semgrep now ship in the `cpu` and `cuda` images**, and
  **a `lint` recipe exists**. §10.1's core loop is "compiler, vet, lint, test,
  race"; `KindLint` was declared, selected by the `high` level, and produced by
  no recipe, so the loop shipped with four of its five legs. The semgrep recipe
  did exist and skipped on every published image, because the binary was not
  there. Both run only where the repository committed a configuration for them
  (`.golangci.yml`, `semgrep/*.yaml`) — a repository that adopted neither gets
  the same `high` verdict as before. A test now asserts every selectable kind
  has a recipe behind it, and CI runs `le task verify --verify high` inside the
  finished image and fails if either recipe is missing or errors.
- **DR-7**, superseding DR-5: the engine is a native tool loop on the DR-4
  provider boundary, not OpenCode. The adapter contract DR-5 argued for is
  unchanged, and DR-5's status note — which still said the engine "is the next
  phase" — is corrected to point at what shipped.

- **First evaluation run against a real model**, published in
  `docs/benchmarks/results/2026-09-14-tasks.md`: 3 tasks × 4 arms × 5 passes,
  60 runs against a local 35B-A3B MoE on disclosed hardware, with the model's
  SHA-256, the llama.cpp build and the server arguments recorded. The run's own
  conclusion is that the task set cannot discriminate between the arms — every
  interval overlaps, and two of three tasks are at ceiling for every arm.
- **`le eval run --repeat N`** and a per-outcome repetition number, because two
  consecutive identical runs disagreed on 4 of 12 cells. The report now leads
  with a count of cells that changed verdict, and a single-pass report states
  that its cells are one sample each — so a missing warning is never read as
  stability.

### Fixed

- **Four defects in the generate check and the proxy, found reviewing the
  diff rather than running it:**
  - **A generator that *created* a file left it in the worktree.** `restore`
    rewrote what changed and never removed what was added — despite a comment
    saying it did — so a schema gaining a table, the commonest case, defeated
    the entire point of snapshot-and-restore.
  - **File modes were not restored.** A generated script came back
    non-executable, and `os.WriteFile` applies a mode only when it *creates* a
    file, so an explicit `chmod` was needed.
  - **A failed restore reported `error`, which does not block acceptance.**
    That is right for a tool that could not run and wrong here: the worktree is
    no longer what any other result measured, so it is now a `fail`.
  - **The proxy's `IdleTimeout` was applied as an absolute deadline**, capping
    a whole tunnel at five minutes. A large `go mod download` over a slow link
    arrived truncated, which reads as a corrupt module rather than a timeout.
    The deadline is extended on every transfer now; a regression test trickles
    a response through a real CONNECT tunnel and fails against the old code.
- **The image was 3,822 MB.** The `go install` layer removed the module
  *download* cache and left 3.2 GB of extracted modules behind, plus a second
  Go toolchain that `buf` asked for. `go clean -cache -modcache -testcache`
  brings the CPU image to 837 MB — 127 MB above the pre-tools baseline, for
  twelve added tools. The difference between removing the download cache and
  removing the module tree is invisible in a Dockerfile review.
- **The `le` binary's own path was not granted to a task sandbox.** In the
  image it sits under `/usr` and is covered; a host install puts it in
  `~/.local/bin`, which is not — so every repository-declared verification step
  would have failed with the sandbox helper exiting 126. Found by running the
  new declared recipes against a bare-metal-style install rather than only in
  the image.
- **A provisioning lane could not write `/dev/null`.** `curl` reported "Failure
  writing output to destination", which says nothing about a missing sandbox
  grant. Device nodes are granted now, as they already were for recipes.
- **The nine golangci-lint findings that were failing CI on `main`.** The
  config is committed and CI runs it unfiltered, so the lint job was red
  independently of any change; shipping the lint recipe made it visible.
  - `errorlint` ×2 in `internal/policy`: `%v` on an error that should be
    wrapped. Both now use a second `%w` (`fmt.Errorf` has taken more than one
    since Go 1.20), so the sentinel stays matchable by `errors.Is` *and* the
    underlying parse error becomes reachable by `errors.As`.
  - `noctx` in `internal/acp`: `net.Listen` cannot be bounded. `acp.Listen`
    now takes a context and uses `net.ListenConfig`, which covers the bind —
    the part that can block when the address carries a hostname.
  - `staticcheck` QF1001 in the TypeScript parser: the negated disjunction is
    now a named condition, which says what the three cases have in common.
  - `contextcheck` in `internal/eval`, `gosec` G703 ×2 in the TypeScript
    sidecar, `nilerr` ×2 in `cmd/le`: suppressed with reasons. The cleanup
    closure must *not* inherit a cancelled context — `Store.Close` builds its
    own precisely so a WAL checkpoint is not skipped when the caller goes
    away — and the G703 taint source is the documented
    `LE_TYPESCRIPT_SIDECAR_DIR`, which is process environment rather than
    repository input.
  - One of the `nilerr` sites already carried `//nolint:nilnil`, naming a
    linter this repository does not enable. It suppressed nothing and hid the
    finding, which is the argument for `nolintlint` — not enabled here,
    because `allow-unused: false` also reports 22 now-inert `gosec`
    directives whose reasoning is worth keeping.
- **Four defects found by running the new lint recipe end to end in the image**,
  none of which the test suite could see because nothing had ever invoked an
  external tool from inside the sandbox:
  - **`sandbox.read_only_paths` granted `/opt/le/toolchain`, a directory no
    image creates.** Everything the installation puts under `/opt/le` — the Go
    tools, semgrep's virtualenv, the TypeScript sidecar — was therefore denied,
    and the symptom was the sandbox helper exiting 126. Now `/opt/le`.
  - **Exit 126 and 127 were reported as `fail`.** They are the POSIX
    exec-failure codes: the tool never ran, so the result says nothing about
    the code. They are `error` now, with a message naming the likely cause,
    because reporting them as `fail` claims code is broken when nothing
    checked it.
  - **A tool that prints text after its JSON defeated the summarizer.**
    golangci-lint follows its report with a plain-text tally, so unmarshalling
    the whole buffer failed with "extra data" and the run fell back to the
    generic summarizer. Summarizers now decode the first JSON value and ignore
    what follows.
  - **The generic fallback could put a whole report into a packet.** It carried
    the last five *lines*, and golangci-lint's entire JSON report is one 3 KB
    line. Every line it carries is now bounded, which is what §8.2 asks of
    tool output everywhere else.
- **A failing `semgrep` run produced an accepted task.** `recipe.Required` does
  not list `analyzer` — correctly, because the recipe is conditional on the
  repository carrying rules, and demanding it unconditionally would fail every
  repository that carries none. But `Accept` only ever looked at the required
  kinds, so a check that ran and found something was discarded. The contract
  now has a fifth rule: **no check that ran may have failed**, whatever its
  kind. An `error` still does not block (a tool that could not run says nothing
  about the code) and neither does a failure recorded against a candidate the
  worktree has moved past. Found by running the new lint recipe end to end in
  the image and noticing the task was accepted anyway; the regression test
  fails against the old `Accept`.
- **The image carried tools §4.2 does not list and lacked ones it does**, with
  no stated rule for which. The rule turns out to be a property of the system:
  the engine has no shell tool, so its only route to an external program is
  `run_recipe`, and a binary no recipe invokes is unreachable rather than
  optional. `docs/reference/image-manifest.md` now carries that rule and a row
  per absent §4.2 tool saying why — including `squawk`, `oasdiff` and `buf`,
  which check a *change* against a baseline while `Recipe.AppliesTo` only ever
  sees a worktree.
- **Documentation claimed an allowlisting egress proxy that does not exist.**
  §6.1 makes it the only route out for the dependency and documentation lanes
  and half of the network containment Landlock's port rules cannot finish.
  Neither the proxy nor the lanes are built, and `le doctor`, the §6.2
  guarantee table, `SECURITY.md`, the README, the isolation and trust-boundary
  pages, the troubleshooting page and the C4 context diagram all described it
  as present — the diagram even drew an arrow to a package registry nothing
  dials. What ships is stricter than the design (no provisioned egress from a
  task sandbox at all, `GOPROXY=off` in every verification run), so nothing was
  weaker than advertised; but a task needing a new dependency has no supported
  path, and that is now said rather than implied away.
- **Stale claims about the project's own state.** The README said language
  analyzers beyond Go were not done, while its own limitations section
  described five of them; the README and `ROADMAP.md` said TypeScript was
  analysed lexically after the sidecar shipped; the roadmap's Phase 5 heading
  said "no results run" directly above the published results and left the
  "run against a real model" box unticked; and `docs/reference/graph-schema.md`
  listed TypeScript as "not yet".
- **`le models bench` reported prefill and decode rates an order of magnitude
  low**, dividing both phases by the same wall clock and counting prompt-cache
  hits as prefill work. On the reference laptop this read 43 tok/s against a
  measured ~416. The rates now come from the provider's own per-phase timings,
  the benchmark prefix carries a per-run nonce so a warm server is not measured
  instead of the model, and the profile records which of the two produced the
  numbers.
- **Budget exhaustion was reported as the model choosing to stop.** A reasoning
  model that spends its whole output budget thinking returns no content and no
  tool call; the engine recorded it as a decision. `ChatResponse` now carries
  the reasoning text and truncation is its own outcome.
- **Tools were advertised to engines that could not run them**, so an arm built
  by leaving retrieval or verification unwired was still offered those tools and
  had every call rejected — charging the baseline for the components it was
  supposed to be measured without.
- **The supervised evaluation arms retrieved from an unindexed workspace.** Each
  run now opens its own workspace, indexes the task copy with the analyzers
  `le index` uses, and refuses up front rather than reporting a zero.
- **`diff_bytes` and `tokens_used` were published but never assigned**, so every
  result carried a zero diff beside a non-zero file count, and every supervised
  row reported no token cost at all.

- **Workspace identity and isolation contract.** A workspace id derived from
  the canonical root, the git remote and a user-supplied name, pinned in
  `.le/workspace.yaml`, with `le workspace adopt` to re-bind after a move.
  Every subsystem stores state under `$LE_DATA/workspaces/<id>/` and nowhere
  else; the storage API cannot be reached without a workspace handle.
- **`storescope` analyzer** enforcing that `sql.Open` and file writes stay
  inside `internal/store` and `internal/artifacts`, so isolation is a build
  failure rather than a convention.
- **Per-workspace SQLite storage**: `index.db`, `ledger.db` and `telemetry.db`
  with forward-only migrations, WAL mode, `synchronous=FULL` for the ledger,
  startup `quick_check`, and a workspace stamp that refuses to open a database
  belonging to another workspace.
- **Typed code graph** with five evidence categories and recursive-CTE
  traversal, plus `impact_of` reporting consumers with deterministic
  compatibility verdicts and migration steps.
- **Retrieval** in the designed order — lexical anchors, then graph expansion,
  then mandatory impact slots — with a guard that rejects any slice from
  another workspace.
- **Execution journal and recovery**: intent written before the side effect and
  outcome after, uncertain operations classified by inspecting the worktree,
  evidence marked stale against an older candidate, and worktree leases that
  expire so a crashed holder's claim is reclaimable.
- **Three-layer sandbox** (container, Landlock, optional bubblewrap) with a
  runner interface, honest per-layer guarantees, and `le doctor` reporting
  which layers are actually active.
- **Process manager** for the single-container process model, with health
  checks, restart budgets with backoff, process-group termination and an
  orderly SIGTERM path.
- **Provider abstraction** over OpenAI-compatible HTTP plus an explicit
  capability declaration, with llama.cpp, generic OpenAI-compatible, OpenAI and
  Anthropic providers, role routing, and offline mode refusing remote providers
  at startup.
- **Hardware profiles as configuration** and `le models bench` to measure a
  machine and generate one.
- **Supervisor API** with `/healthz`, `/readyz`, `/v1/status`, `/v1/sandbox`
  and a dependency-free dashboard.
- **Container images** (slim, cpu, cuda targets), default and split compose
  files, and an entrypoint that reports the active isolation layers before
  starting.

- **Go language analyzer** producing the compiler-backed rows of the coverage
  table: modules, packages, imports, the call graph, interface satisfaction via
  `types.Implements`, type usage, signatures, struct fields and embedding,
  tests, configuration keys and HTTP routes. Interface dispatch is
  over-approximated with class hierarchy analysis and labelled `inferred` with
  the assumption recorded on the edge, so impact analysis reports those
  consumers as undetermined rather than making a claim it cannot support.
- **Commit-history analyzer** contributing `observed` commit-to-file edges,
  with sweeping commits skipped because a vendor drop says nothing about
  coupling.
- **Per-task git worktrees**, so a task never edits the operator's checkout,
  and `SyncFrom` to carry uncommitted work into one deliberately.
- **Verification recipes** for build, vet, test, race and format, each with a
  summariser that compresses output at source: the full log becomes a
  content-addressed artifact and only the findings travel.
- **Verification levels and the completion contract.** A task is accepted only
  when every recipe its level requires has a passing result produced against
  the current candidate, with no out-of-scope writes. A skip, an error, or a
  pass against an older candidate satisfies nothing.
- **Engine adapter contract** (`internal/engine`) with a verification-only
  implementation, so the whole pipeline runs and is tested without a model.
- `le task verify`, `le task create` and `le task run`.

- **Tool calling in the provider layer**, across both wire formats: the
  OpenAI-compatible function envelope and the Messages API's content blocks,
  including replaying tool calls on assistant turns and merging tool results
  into a single user turn.
- **The editing engine** (`internal/engine/native`): a bounded tool loop with
  nine tools — read, edit, write, list, search, find symbol, impact, run
  verification, done. Every file operation is confined to the task worktree by
  `internal/worktree` as well as by the sandbox, tested against traversal and
  symlink escapes. A failed tool call is a result the model can act on, not an
  error that discards the turn.
- **Task decomposition** into bounded child tasks with dependencies. A plan is
  produced by a model and validated by the supervisor: a step with no scope, a
  scope escaping the repository, an unknown verification level, or a forward
  dependency is rejected before anything runs.
- **The broker and human gates.** A gate carries the deterministic evidence —
  impact report, diff, verification findings — and is journalled before it
  blocks, so an interrupted approval is a pending gate on restart rather than a
  lost one. Which decisions gate is configuration; a budget increase always is.
- `le plan`, `le gate list/show/approve/reject`.

- **Schema analyzer**: a pure-Go PostgreSQL DDL parser over migrations,
  applied in file order so the result is the schema as it ends up. Statements
  outside the subset are counted and reported rather than skipped silently.
- **Schema to application code**: type-checked database call sites, so a table
  change finds the queries that read it. A literal query is `resolved`; a
  wrapper driver matched by method name is `inferred`; a query assembled at
  runtime names no table at all.
- **Deployment and build analyzers**: compose services and their environment,
  Kubernetes workloads and ConfigMaps, Dockerfile stages and `COPY` sources,
  Makefile targets. Helm templates are recorded as present but not parsed,
  because rendering needs chart values.
- **Terraform analyzer**: resources, modules, variables and the references
  between them, plus the environment variables they set.
- An impact report now crosses analyzer boundaries: changing a configuration
  key finds the Go code, the compose service, the Dockerfile stage and the
  Terraform resource in one traversal.

### Fixed

- **Edge directions that made consumers invisible.** Impact analysis is a
  reverse traversal, so an edge pointing the wrong way is not wrong in any
  visible way — the report simply comes back short. `implements` pointed
  interface → type, so adding a method to an interface reported **zero**
  implementations; `reads_config` pointed key → reader in the Go analyzer but
  reader → key everywhere else, so a config change found the manifests that
  set a variable but not the code that read it. Both corrected, the invariant
  is stated where edge kinds are defined, and a cross-analyzer test guards it.
- **`#` or `?` in a data-directory path silently opened the wrong database.**
  The SQLite DSN concatenated the path into a `file:` URI, where `#` starts a
  fragment and `?` starts a query — so the path was truncated, and two
  workspaces differing only after that character resolved to the *same file*.
  Both opened successfully, so nothing downstream could detect it. The path is
  now escaped into the URI.
- **Analyzer output order was load-bearing.** Edges were resolved per analyzer,
  so a cross-analyzer edge was dropped unless its target happened to be written
  first. The indexer now runs every analyzer, writes every node, then resolves
  every edge.

- **Evaluation harness** (`internal/eval/`, `evals/tasks/`, `le eval`):
  - Acceptance tests are never in the worktree while a task runs. A model that
    can read the test can satisfy it without solving the problem, and that
    failure looks exactly like success in the numbers.
  - The system's own verdict is recorded separately from the ground truth, so
    "claimed success and was wrong" is its own rate rather than something
    averaged away.
  - Protected paths are checked: a task passed by deleting the failing test is
    counted as unsolved.
  - A run that errors is excluded from every rate, because a harness fault is
    not evidence about the system.
  - Arms for the unsupervised baseline, the full system, and ablations of the
    graph and of the verification loop — structurally different pipelines, not
    flags the system might ignore.
  - Wilson confidence intervals on every rate, and a difference called
    significant only when the intervals do not overlap.
  - Task-set validation in CI: acceptance must fail on the untouched fixture,
    a reference solution must pass, fixtures must start green, and objectives
    must not name the fix.

### Known gaps

- TypeScript has no analyzer. It needs a Node sidecar for the TS compiler API.
- **No task-success numbers have been produced.** The harness is built and
  tested; a run against a real model on disclosed hardware has not been done.
  Until `docs/benchmarks/results/` contains them, nothing here claims a success
  rate, and the graph's contribution is measurable rather than measured.
- [The graph schema reference](docs/reference/graph-schema.md) marks the state
  per language.
- The design chose OpenCode as the execution engine (DR-5). What shipped is a
  native engine over the provider boundary: the adapter contract is what DR-5
  is about, and a native implementation is testable end to end without an
  external process. An OpenCode adapter remains a second implementation of the
  same interface; the record should be superseded rather than ignored if the
  native engine turns out to be the permanent answer.

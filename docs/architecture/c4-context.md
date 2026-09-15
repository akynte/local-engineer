# C4 level 1: system context

Who uses local-engineer, and what it talks to.

```mermaid
C4Context
  title System context

  Person(dev, "Developer", "Works in a repository on their own machine. Wants a task done and wants to see the evidence before it lands.")

  System(le, "local-engineer", "Supervises a local model against one repository at a time. Decides acceptance from verification evidence, never from the model's claim.")

  System_Ext(repo, "Repositories", "The developer's own code, bind-mounted. The only source local-engineer edits.")
  System_Ext(model, "Inference server", "llama.cpp, vLLM, or any OpenAI-compatible endpoint. Local by default.")
  System_Ext(editor, "Editor", "Optional. Reaches the agent over the ACP bridge.")
  System_Ext(registry, "Package registries", "Reached only by a §6.1 provisioning lane, through the allowlisting proxy, and only for hosts named in egress.allowlist. Never from a task sandbox.")

  Rel(dev, le, "Gives a requirement; approves or rejects at a gate", "CLI / HTTP on 127.0.0.1:7777")
  Rel(le, repo, "Reads; edits in a per-task worktree")
  Rel(le, model, "Chat, structured output, embeddings", "OpenAI-compatible HTTP")
  Rel(editor, le, "Agent session", "ACP over TCP")
  Rel(le, registry, "le deps / le docs only, allowlisted hosts", "HTTPS via the §6.1 proxy")

  UpdateLayoutConfig($c4ShapeInRow="2", $c4BoundaryInRow="1")
```

## What this level is for

Two boundaries are the point of the picture, and both are constraints rather
than connections:

**The developer is in the loop by design.** The arrow from the developer is not
"starts a job" — it is "approves or rejects at a gate". §3.3 puts an impact
report and a diff in front of a person before a change is applied. A system that
applied its own changes would need a different context diagram and a different
trust argument.

**Nothing reaches the network from a task.** The registry arrow comes from a
§6.1 provisioning lane — `le deps` or `le docs`, run by an operator between
tasks — not from a sandbox running one. Verification runs with `GOPROXY=off`,
and a task's Landlock ruleset never includes the proxy port. Drawing one arrow
for "the internet" would hide the only part of this worth knowing: which side
of the boundary the arrow starts on.

## What is deliberately absent

There is **no telemetry endpoint, no license server, and no account**. Telemetry
is a local SQLite file (§5.2). The system runs with `--network none` plus an
in-container inference route, and the tutorial says so.

Next: [container view](c4-container.md).

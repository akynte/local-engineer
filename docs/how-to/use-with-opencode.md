# Use Local Engineer from OpenCode

Local Engineer can answer questions about a repository from inside
[OpenCode](https://opencode.ai), so day-to-day use does not need the terminal.

## What this gives you

Four tools become available to the agent. You ask in words; it calls them.

| Ask something like | Tool it uses |
|---|---|
| "is Local Engineer set up here?" | `le_status` |
| "what breaks if I change `Total`'s signature?" | `le_graph_impact` |
| "where is stock reserved?" | `le_search` |
| "the index looks stale, refresh it" | `le_reindex` |

`le_status` also reports what is wrong: never indexed, index behind the working
tree, or a workspace pinned at a path it has since moved from.

## Setting it up

Local Engineer must be on your `PATH`. Check with `le version`.

Add the server to `opencode.jsonc`, either in the repository or in
`~/.config/opencode/`:

```jsonc
{
  "mcp": {
    "local-engineer": {
      "type": "local",
      "command": ["le", "mcp"],
      "enabled": true
    }
  }
}
```

If your data directory is not the default, pass it:
`"command": ["le", "mcp", "--data", "/path/to/le-data"]`.

Then open the repository in OpenCode and ask it whether Local Engineer is set
up. If the repository has no workspace yet, run `le workspace init` and
`le index` once in the terminal; after that the tools work.

## What is deliberately not here

Running tasks and every destructive operation stay on the CLI.

`le task run` drives a model through a bounded tool loop for minutes, edits a
confined worktree, and ends at a human gate carrying the diff. Exposing it as a
tool would put that budget under another agent's control and move the gate out
of the place a person is watching. Backup, restore, note deletion and dependency
fetching are absent for the same reason: they are decisions, not lookups.

So the split is a deliberate one rather than an unfinished surface. OpenCode is
where you ask what the repository is like. The terminal is where you tell Local
Engineer to change something.

## How it relates to the CLI

Both call the same code. A tool does not shell out to `le`: it binds to the
workspace through `internal/session` — the same binding the CLI performs,
including the §2.2 switch that clears the previous workspace's inference slots —
and then calls `internal/graph`, `internal/retrieval` and `internal/index`
directly. There is no second implementation to drift.

Nothing about the CLI changes. Every command works as before.

## Troubleshooting

**The tools do not appear.** OpenCode starts the server as a subprocess; if `le`
is not on `PATH` it fails silently from the editor's point of view. Run
`le mcp` in a terminal: it should print `local-engineer mcp: serving <path> over
stdio` to stderr and then wait. Ctrl-C to exit.

**Every tool says the directory is not a workspace.** The server resolves the
workspace from the directory OpenCode started it in. Open the repository root,
or pass `path` to a tool naming a subdirectory of it.

**`le_graph_impact` cannot find a symbol that exists.** The index is behind the
code. Run `le_reindex`, or `le index` in the terminal.

**Answers look out of date.** `le_status` reports how many scopes changed since
the last index. A non-zero count means the graph describes older code.

## Security

The `path` argument a tool accepts is confined to the directory OpenCode opened.
Absolute paths are refused, `..` is refused, and a symlink pointing out of the
repository is refused after resolution — so a tool call cannot reach a
repository you did not open, and repository content that reaches the model
cannot turn into one that does.

Tools never build a shell command. There is no path from a tool argument to a
shell, which is why argument handling here is a confinement problem rather than
a quoting problem.

Each call rebinds to one workspace and caches no handle between calls. Opening
project A and then project B cannot let B read A's index.

## Supported versions

This uses the Model Context Protocol via the
[official Go SDK](https://github.com/modelcontextprotocol/go-sdk), on the
protocol version that SDK implements. Any MCP client can use it; OpenCode is the
one documented here because its `"mcp"` block is the officially supported way to
register a local server.

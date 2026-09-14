# Workflows

**This directory is empty on purpose, and that is a finding rather than an
omission.**

§1.2 lists `workflows/` in the repository layout and never says what goes in it.
It appears in no other section: no format, no loader, no command, no exit
condition references it. v3 defers several things to v2.0, which is not in this
repository — the acceptance matrix and the evaluation stages are the other two —
and this looks like a third.

Two things could reasonably live here, and both already live somewhere better:

- **Task decomposition**, if a "workflow" meant a plan template. `le plan`
  already decomposes a requirement into bounded child tasks with dependencies
  (§10.1), and it derives them from the repository rather than from a template,
  which is the part that makes it useful.
- **Verification sequences**, if a "workflow" meant an ordered set of checks.
  That is what a recipe and a verification level already are (§10.1), and they
  are Go rather than YAML because ordering is load-bearing — build first, so a
  compile failure short-circuits the rest.

Inventing a third mechanism to fill a directory would add a concept the design
never asked for. So this stays empty, with the reason written down, until either
v2.0's definition is available or a real need names itself.

See `ROADMAP.md` for the other things v3 defers to a document this repository
does not have.

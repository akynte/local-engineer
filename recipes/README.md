# Recipes

Recipes are **Go, in `internal/recipe`**, not YAML here.

§1.2 lists `recipes/` in the layout, and the deliberate choice is worth stating
because the directory being thin looks like something unfinished.

A recipe is a command, a timeout, an environment, a predicate deciding whether
it applies, and — the part that matters — a **summariser** that turns tool
output into the compressed form a next step can use. §8.2 calls this
"compression of tool output at source", and it is the reason recipes are not
data:

- Parsing `go test` output into per-test findings, `go vet` into diagnostics, a
  race report into the two conflicting stacks, is code. Expressing it in YAML
  would mean a pattern language, and a pattern language is a worse Go.
- Ordering is load-bearing. Build runs first so a compile failure
  short-circuits the rest rather than producing a page of test noise about code
  that never built. That is a property of the set, not of any entry.
- A recipe's result is evidence the completion contract judges. Evidence whose
  shape is configurable is evidence whose meaning changes with configuration.

What *is* configurable is which recipes run: verification levels (`low`,
`standard`, `high`) select by kind, per task.

## Adding one

See [`docs/how-to/write-a-recipe.md`](../docs/how-to/write-a-recipe.md). The
short version: add it to `GoRecipes` with a `Kind`, an `AppliesTo` predicate so
it skips repositories it cannot check, and a summariser — and make the
summariser report a tool that *failed to run* as an error rather than a pass.
"Your code is fine" and "we did not manage to check it" are different sentences.

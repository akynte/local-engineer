# Schemas

§1.2 lists "schema validation (all JSON Schemas compile; all YAML policies
validate)" as a CI gate. These are the schemas it validates.

They describe the files a **user** writes, not the wire formats between internal
packages. A Go struct is already the authority for those, and a second
description of the same thing is a second thing to keep in step.

| Schema | Describes |
|---|---|
| [`le.schema.json`](le.schema.json) | `/data/config/le.yaml`, the operator configuration |
| [`providers.schema.json`](providers.schema.json) | `/data/config/providers.yaml`, provider and role routing |
| [`profile.schema.json`](profile.schema.json) | a hardware profile under `profiles/` |
| [`task.schema.json`](task.schema.json) | an evaluation task under `evals/tasks/` |
| [`workspace.schema.json`](workspace.schema.json) | `.le/workspace.yaml`, the identity pin |

## What keeps them honest

A schema that drifts from the code it describes is worse than none: it says a
file is valid that the program then rejects, and the error the user gets comes
from somewhere else entirely.

So the test in `internal/config/schema_test.go` does three things, and the third
is the one that matters:

1. every schema compiles;
2. every shipped example validates against its schema — the default config, all
   nine profiles, every task in the evaluation set;
3. a file the **schema** accepts must also be accepted by the **loader**, and a
   file the loader rejects must be rejected by the schema.

Point 3 is what stops the two drifting apart. Adding a required field to a
struct without adding it here fails the test.

## Editor support

Point an editor at a schema with a modeline, and configuration gets completion
and inline errors:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/akynte/local-engineer/main/schemas/le.schema.json
```

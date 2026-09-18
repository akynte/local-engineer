# Write a policy rule

A policy says what **no task** may change, whatever it was asked to do.

Scope declares where a task may write. Repository policy independently protects
paths even inside that scope. The native editor checks both before writing;
other editing engines are checked against their final diff. An empty task scope
grants no native editor writes.

## The file

Policies live in `policies/` in the repository, so they travel with it and a
clone carries them.

```yaml
name: shipped-protected-paths
protected:
  - path: .github/workflows/**
    reason: >-
      These decide whether a change is accepted. A task that can edit them can
      approve its own work, which makes every other check advisory.
```

`a/**` protects a whole subtree. A bare directory name does too, because that is
what someone writing `policies` rather than `policies/**` means.

## Every rule needs a reason

The loader refuses a rule without one, and not for tidiness. When a rule fires,
the operator sees the reason at the gate and decides whether to allow the change
anyway. A rule that cannot say why it exists gives them nothing to decide with,
so it gets allowed — and a rule that is always allowed is not a rule.

Write the reason as what would go wrong, not as a restatement of the path:

```yaml
# Not this:
  - path: policies/**
    reason: policies are protected

# This:
  - path: policies/**
    reason: >-
      A task that can edit the policy can remove the rule stopping it. The
      restriction has to be outside what it restricts.
```

## Checking it

```console
$ le task run <task-id>
policies: 1 rule(s) protecting 5 path pattern(s)
```

A change to a protected path is reported as out-of-scope with the reason
attached, so acceptance refuses it and the gate explains why.

Policy files are validated against
[`schemas/policy.schema.json`](https://github.com/akynte/local-engineer/blob/main/schemas/policy.schema.json) in CI, which is
what §1.2's "all YAML policies validate" gate means. A malformed policy fails the
build rather than silently protecting nothing.

## What a policy cannot do

It cannot stop a task reading anything, and it is not a security boundary. A
task runs inside the container and the sandbox (§6.2); the policy is about
*intent*, catching a change that is legal, compiles, and is still not something
a task should be making on its own.

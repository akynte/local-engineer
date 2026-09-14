# Policies

Repository-wide rules about what **no task** may change, whatever it was asked
to do.

This is not scope. A task declares the scope its work belongs in, and writes
outside that scope are detected and gated. But a task that declares no scope was
unrestricted — `OutOfScope` returns nothing when the allowed list is empty — so
"change whatever you like" was the default for an undeclared task.

§6.2 lists "cannot modify policy, ledger, hidden tests" as a guarantee of the
sandbox, and inside a worktree the sandbox cannot provide it: the task
legitimately has write access to the checkout it is editing. That is the gap
these files close.

## Format

```yaml
name: a short name, shown when a rule fires
protected:
  - path: .github/workflows/**
    reason: >-
      Why this matters. The operator reads this at the gate, so it should say
      what would go wrong rather than restate the path.
```

`a/**` protects a whole subtree. A bare directory name does too, because that is
what an operator writing `policies` rather than `policies/**` means.

Every rule needs a reason. A rule that cannot say why it exists is one nobody
can judge, and the reason is the only part of it a person reads when deciding
whether to allow the change anyway.

Files here are validated against
[`schemas/policy.schema.json`](../schemas/policy.schema.json) in CI, which is
what §1.2's "all YAML policies validate" gate means.

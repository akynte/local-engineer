# DR-6: Workspace identity as the isolation key

- Status: Accepted
- Date: 2026-09-14

## 1. Problem

Never confuse projects. Two repositories with the same file names and the same
symbol names must never share an index, a cache, a session, or a prompt cache
slot.

## 2. Alternatives

- Path-keyed directories.
- Git-remote-keyed directories.
- An explicit id file pinned in the repository.

## 3. Evidence

Paths move: a checkout gets relocated, a worktree gets created elsewhere, a
container mounts the same code at a different path. Remotes change: a fork, a
migration, a rename. Either alone silently re-keys a project's entire history.
An explicit pinned id with an adopt command survives both, and makes the "this
is a different project now" case an explicit act rather than an accident.

## 4. Chosen

A hash-derived id pinned in `.le/workspace.yaml`:

```
workspace_id = base32(sha256(canonical_root_path ‖ git_remote_url_if_any ‖ user_supplied_name))[:26]
```

with separate database files per workspace and a storage API scoped by handle.

## 5. Why

Isolation is enforced by file separation and by type, not by discipline. The
storage API has no call that reaches a table without a workspace handle, and a
custom analyzer keeps `sql.Open` and file writes inside the two packages
allowed to perform them. A database file also carries the id of the workspace
that created it, so restoring a backup into the wrong workspace fails at open
rather than serving another project's code.

## 6. Disadvantages

- **Duplicate indexing if a repository is used in two workspaces.** That is the
  price of never sharing; the design accepts it.
- **An id file to keep in the repository.** It must be committed, and a
  developer who deletes it creates a new workspace — which is the documented
  behaviour, but still a sharp edge.
- A truncated hash is 26 base32 characters, 130 bits. Collision is not a
  practical concern, but it is a truncation.

## 7. Replacement path

The id scheme is versioned (`version.WorkspaceIDScheme`, and `scheme_version`
in the manifest). Opening a workspace written by a newer scheme fails with a
message rather than misreading it, so a future re-keying migration has a
defined starting point.

**Implementation note.** The components are hashed length-prefixed, not
separated by a delimiter. A fuzz test found that NUL-separated components
collide when a component itself contains a NUL — `("\x00", "0", "0")` and
`("", "\x000", "0")` produced the same id. Length prefixing is injective for
any content. The regression seed is committed in
`internal/workspace/testdata/fuzz/`.

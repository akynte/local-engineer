-- ledger.db, schema 2. Human gates (design v3 §3.3).
--
-- A gate is recorded before it blocks, so an interrupted approval is a pending
-- gate on restart rather than a lost one. The evidence column holds what the
-- decision rests on — an impact report, a diff, the verification findings —
-- assembled by the supervisor, so a person reads the deterministic answer
-- rather than a model's account of it.

CREATE TABLE gates (
  id           TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  task_id      TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('plan','impact','out_of_scope','budget','apply')),
  question     TEXT NOT NULL,
  evidence     TEXT NOT NULL DEFAULT '{}',
  decision     TEXT NOT NULL DEFAULT 'pending'
                 CHECK (decision IN ('pending','approved','rejected','expired')),
  decided_by   TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  decided_at   INTEGER,
  expires_at   INTEGER
) STRICT;

CREATE INDEX gates_pending ON gates(created_at) WHERE decision = 'pending';
CREATE INDEX gates_by_task ON gates(task_id);

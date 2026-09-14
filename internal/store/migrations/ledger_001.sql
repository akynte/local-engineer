-- ledger.db, schema 1. Task ledger, execution journal, checkpoints, evidence
-- (design v3 §5.2 and §7.1). synchronous=FULL: this file is the recovery
-- record and must survive a hard kill.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

CREATE TABLE requirements (
  id           TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  title        TEXT NOT NULL,
  body         TEXT NOT NULL DEFAULT '',
  acceptance   TEXT NOT NULL DEFAULT '[]',  -- JSON array of executable checks
  state        TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','in_progress','accepted','rejected','abandoned')),
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE tasks (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  requirement_id TEXT REFERENCES requirements(id) ON DELETE SET NULL,
  parent_id     TEXT REFERENCES tasks(id) ON DELETE CASCADE,
  title         TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'change',
  worktree_id   TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'pending' CHECK (state IN (
                  'pending','running','paused','blocked','review','accepted','failed','abandoned')),
  verification  TEXT NOT NULL DEFAULT 'standard' CHECK (verification IN ('low','standard','high')),
  budget        TEXT NOT NULL DEFAULT '{}', -- JSON: steps, tokens, wall clock
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  finished_at   INTEGER
) STRICT;
CREATE INDEX tasks_by_state ON tasks(state);
CREATE INDEX tasks_by_requirement ON tasks(requirement_id);

CREATE TABLE task_deps (
  task_id   TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  depends_on TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  PRIMARY KEY (task_id, depends_on)
) STRICT;

-- §7.1, verbatim structure. Every model-visible action is an operation; intent
-- is written BEFORE the side effect, outcome AFTER. NULL outcome means
-- uncertain and drives reconciliation (§7.2 step 1).
CREATE TABLE operations (
  id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, seq INTEGER NOT NULL,
  kind TEXT NOT NULL,          -- inspect_file, search, retrieval, decision, edit, recipe_run, review, checkpoint, approval, session_start, session_end
  intent TEXT NOT NULL,        -- written BEFORE the side effect (JSON)
  outcome TEXT,                -- written AFTER; NULL means uncertain (JSON)
  candidate_before TEXT, candidate_after TEXT,   -- content manifest hashes
  evidence_id TEXT, started_at INTEGER, finished_at INTEGER,
  UNIQUE (task_id, seq)
) STRICT;
CREATE INDEX operations_uncertain ON operations(task_id) WHERE outcome IS NULL;
CREATE INDEX operations_by_kind ON operations(task_id, kind);

CREATE TABLE checkpoints (
  task_id TEXT NOT NULL, seq INTEGER NOT NULL, state TEXT NOT NULL,
  handoff TEXT NOT NULL, candidate TEXT, created_at INTEGER NOT NULL,
  PRIMARY KEY (task_id, seq)
) STRICT;

CREATE TABLE leases (
  worktree_id TEXT PRIMARY KEY, task_id TEXT, holder TEXT, expires_at INTEGER
) STRICT;

-- Evidence rows reference content-addressed artifacts under
-- workspaces/<id>/artifacts/<sha256> (§2.2).
CREATE TABLE evidence (
  id            TEXT PRIMARY KEY,
  task_id       TEXT NOT NULL,
  kind          TEXT NOT NULL,             -- build, test, vet, lint, race, analyzer, runtime, review, diff
  status        TEXT NOT NULL CHECK (status IN ('pass','fail','error','skipped','stale')),
  candidate     TEXT NOT NULL DEFAULT '',  -- content manifest hash this evidence was produced against
  artifact_hash TEXT NOT NULL DEFAULT '',  -- sha256 key into artifacts/
  summary       TEXT NOT NULL DEFAULT '',
  detail        TEXT NOT NULL DEFAULT '{}',-- JSON, structured summarizer output
  created_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX evidence_by_task ON evidence(task_id, kind);
CREATE INDEX evidence_by_candidate ON evidence(candidate);

-- Structured handoffs replace chat summaries (§8.2).
CREATE TABLE handoffs (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  reason     TEXT NOT NULL,                -- pause, shutdown, crash, context_restart, model_failure
  body       TEXT NOT NULL,                -- JSON working state
  created_at INTEGER NOT NULL,
  UNIQUE (task_id, seq)
) STRICT;

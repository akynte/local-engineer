-- Architecture review §§7–8, 11, 20: durable phase state and counters.
CREATE TABLE task_workflow (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  phase TEXT NOT NULL CHECK (phase IN ('INTAKE','LOCALIZE','IMPACT','PLAN','EDIT','VERIFY','REVIEW','FINALIZE')),
  body TEXT NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

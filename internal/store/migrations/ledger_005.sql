-- ledger.db, schema 5. Bind a gate to the content it was shown.
--
-- A gate's id carried a timestamp and nothing tied it to the change it was
-- asked about, so re-running a task after an approval opened a second gate
-- instead of honouring the first. The task could never finalize: every run
-- ended at a fresh gate, and approving it only made another one.
--
-- The candidate is the content manifest hash the gate's evidence describes. A
-- decision is reused only for that exact content, so approving a diff can never
-- approve a later, different one — which is the reason the gate exists.
ALTER TABLE gates ADD COLUMN candidate TEXT NOT NULL DEFAULT '';

CREATE INDEX gates_by_task_kind_candidate ON gates(task_id, kind, candidate);

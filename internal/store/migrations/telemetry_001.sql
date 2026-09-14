-- telemetry.db, schema 1 (design v3 §5.2). Counters and timings only; the
-- optional aggregate holds workspace ids and counters, never content (§2.2).

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

CREATE TABLE events (
  id           INTEGER PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  task_id      TEXT NOT NULL DEFAULT '',
  ts           INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  count        INTEGER NOT NULL DEFAULT 1,
  attrs        TEXT NOT NULL DEFAULT '{}'  -- JSON: numbers and enums, no source content
) STRICT;
CREATE INDEX events_by_ts ON events(ts);
CREATE INDEX events_by_name ON events(name, ts);

CREATE TABLE gpu_samples (
  id            INTEGER PRIMARY KEY,
  ts            INTEGER NOT NULL,
  device        INTEGER NOT NULL DEFAULT 0,
  vram_used_mb  INTEGER NOT NULL,
  vram_total_mb INTEGER NOT NULL,
  utilization   INTEGER NOT NULL DEFAULT 0,
  power_w       REAL NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX gpu_samples_by_ts ON gpu_samples(ts);

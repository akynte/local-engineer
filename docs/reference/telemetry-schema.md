# Telemetry schema

What `telemetry.db` holds, and what it deliberately does not.

## What is recorded

One SQLite file per workspace, at `workspaces/<id>/telemetry.db`.

### `events`

| Column | Meaning |
|---|---|
| `id` | row id |
| `at` | unix milliseconds |
| `kind` | what happened: `task_started`, `recipe_run`, `gate_opened`, `packet_built`, … |
| `task_id` | the task, when there is one |
| `duration_ms` | how long it took |
| `counts` | JSON of integers: tokens, findings, slices, retries |

### `gpu_samples`

| Column | Meaning |
|---|---|
| `at` | unix milliseconds |
| `vram_used_mb`, `vram_total_mb` | sampled during generation |
| `ram_used_mb`, `ram_total_mb` | host memory |

Sampled so that a profile's `measured_peak_vram_mb` comes from observation
rather than a guess, which is what §9.3 requires of every memory budget.

## What is not recorded

**No content.** Not source, not prompts, not model output, not paths beyond a
repository-relative name where one is needed to make an event meaningful. The
aggregate form §2.2 allows holds "counters, never content", and the same rule
applies to the per-workspace file.

**Nothing leaves the machine.** There is no endpoint, no key, and no upload
path. Telemetry is a local file you can read with `sqlite3`, and deleting it
costs you history and nothing else.

## Reading it

```console
$ sqlite3 "$LE_DATA/workspaces/<id>/telemetry.db" \
    "SELECT kind, COUNT(*), ROUND(AVG(duration_ms)) FROM events GROUP BY kind;"
```

The dashboard charts packet size per step from the same table, which is §8.3's
measurement of whether the packet cap is right for a profile.

## Retention

`le doctor` reports the file's size. There is no automatic expiry: a counter
table stays small, and silently deleting a user's history to save a few
megabytes is a worse default than letting them run `le backup` and delete it
themselves.

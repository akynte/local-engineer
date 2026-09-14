-- index.db, schema 1. Source index, symbol index, graph, chunks, FTS,
-- embeddings (design v3 §5.2). One file per workspace: cross-database queries
-- do not exist in this codebase (§2.2).

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

-- Repositories inside this workspace (§2.1: multi-repo systems are one
-- workspace with several repositories, each with its own repository id).
CREATE TABLE repositories (
  repository_id  TEXT PRIMARY KEY,
  workspace_id   TEXT NOT NULL,
  name           TEXT NOT NULL,
  rel_path       TEXT NOT NULL UNIQUE,
  remote         TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT 'main'
) STRICT;

-- Worktrees: a repository may be checked out more than once (task worktrees).
CREATE TABLE worktrees (
  worktree_id   TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
  rel_path      TEXT NOT NULL,
  branch        TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE files (
  file_id       INTEGER PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
  worktree_id   TEXT NOT NULL DEFAULT '',
  path          TEXT NOT NULL,             -- repository-relative, slash separated
  lang          TEXT NOT NULL DEFAULT '',
  size_bytes    INTEGER NOT NULL DEFAULT 0,
  mode          INTEGER NOT NULL DEFAULT 0,
  content_hash  TEXT NOT NULL,             -- sha256 of file contents
  indexed_at    INTEGER NOT NULL,
  index_version INTEGER NOT NULL,
  UNIQUE (repository_id, worktree_id, path)
) STRICT;
CREATE INDEX files_by_hash ON files(content_hash);
CREATE INDEX files_by_lang ON files(lang);

-- Typed node table. kind covers the coverage list in §2.4.
CREATE TABLE nodes (
  node_id       INTEGER PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
  worktree_id   TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL CHECK (kind IN (
                  'file','directory','package','module','service','api','route','handler',
                  'type','function','method','class','interface','field','variable','constant',
                  'dependency','config_key','infra_resource','deployment','test','build_target',
                  'schema','table','column','commit','doc')),
  name          TEXT NOT NULL,
  fqn           TEXT NOT NULL,             -- fully qualified name, unique within a repository
  file_id       INTEGER REFERENCES files(file_id) ON DELETE CASCADE,
  start_line    INTEGER NOT NULL DEFAULT 0,
  end_line      INTEGER NOT NULL DEFAULT 0,
  signature     TEXT NOT NULL DEFAULT '',
  visibility    TEXT NOT NULL DEFAULT '',
  content_hash  TEXT NOT NULL DEFAULT '',
  attrs         TEXT NOT NULL DEFAULT '{}',-- JSON
  index_version INTEGER NOT NULL,
  UNIQUE (repository_id, worktree_id, kind, fqn)
) STRICT;
CREATE INDEX nodes_by_name ON nodes(name);
CREATE INDEX nodes_by_file ON nodes(file_id);
CREATE INDEX nodes_by_kind ON nodes(kind, name);

-- Typed edges with evidence categories (§3.2). A missing edge means "not
-- discovered" — never "does not exist" (§3.3).
CREATE TABLE edges (
  edge_id      INTEGER PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  src_id       INTEGER NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
  dst_id       INTEGER NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN (
                 'contains','imports','depends_on','calls','implements','uses_type','references',
                 'routes_to','handles','reads_config','writes_schema','reads_schema','tests',
                 'builds','deploys','provisions','touches','extends','embeds','returns','accepts')),
  evidence     TEXT NOT NULL CHECK (evidence IN ('resolved','declared','inferred','observed','unknown')),
  source       TEXT NOT NULL DEFAULT '',   -- which analyzer produced it
  confidence   REAL NOT NULL DEFAULT 1.0,
  attrs        TEXT NOT NULL DEFAULT '{}', -- JSON, e.g. VTA assumptions
  index_version INTEGER NOT NULL,
  UNIQUE (src_id, dst_id, kind, source)
) STRICT;
CREATE INDEX edges_forward ON edges(src_id, kind, evidence);
CREATE INDEX edges_reverse ON edges(dst_id, kind, evidence);

CREATE TABLE chunks (
  chunk_id      INTEGER PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
  file_id       INTEGER NOT NULL REFERENCES files(file_id) ON DELETE CASCADE,
  node_id       INTEGER REFERENCES nodes(node_id) ON DELETE SET NULL,
  start_line    INTEGER NOT NULL,
  end_line      INTEGER NOT NULL,
  content_hash  TEXT NOT NULL,
  token_estimate INTEGER NOT NULL DEFAULT 0,
  index_version INTEGER NOT NULL
) STRICT;
CREATE INDEX chunks_by_file ON chunks(file_id, start_line);

-- FTS5 is the lexical-anchor stage of retrieval (§8.2: lexical anchors first,
-- then graph expansion). External-content table keyed on chunks.chunk_id.
CREATE VIRTUAL TABLE chunks_fts USING fts5(
  body,
  path UNINDEXED,
  symbol,
  tokenize = 'unicode61 remove_diacritics 2'
);

-- Embeddings stay optional (§8.2: enabled only if Stage E shows benefit).
-- Brute-force scan in Go is the default; sqlite-vec is an optional extension.
CREATE TABLE embeddings (
  chunk_id   INTEGER PRIMARY KEY REFERENCES chunks(chunk_id) ON DELETE CASCADE,
  model      TEXT NOT NULL,
  dims       INTEGER NOT NULL,
  vector     BLOB NOT NULL,               -- float32 little-endian
  created_at INTEGER NOT NULL
) STRICT;

-- Index keys: content manifest, lockfile hashes, toolchain, build mode and
-- indexer version (§3.4). A changed key marks the unit dirty.
CREATE TABLE index_keys (
  scope         TEXT NOT NULL,            -- 'repository' | 'package' | 'file'
  scope_id      TEXT NOT NULL,
  manifest_hash TEXT NOT NULL,
  lockfile_hash TEXT NOT NULL DEFAULT '',
  toolchain     TEXT NOT NULL DEFAULT '',
  build_mode    TEXT NOT NULL DEFAULT '',
  index_version INTEGER NOT NULL,
  dirty         INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (scope, scope_id)
) STRICT;
CREATE INDEX index_keys_dirty ON index_keys(dirty) WHERE dirty = 1;

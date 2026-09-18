CREATE TABLE scip_symbols (
 workspace_id TEXT NOT NULL, repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
 symbol TEXT NOT NULL, display TEXT NOT NULL, kind TEXT NOT NULL, file TEXT NOT NULL,
 line INTEGER NOT NULL, signature TEXT NOT NULL, documentation TEXT NOT NULL,
 content_hash TEXT NOT NULL, node_id INTEGER REFERENCES nodes(node_id) ON DELETE CASCADE,
 PRIMARY KEY(repository_id,symbol)
) STRICT;
CREATE INDEX scip_symbols_display ON scip_symbols(display);
CREATE TABLE scip_occurrences (
 workspace_id TEXT NOT NULL, repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
 symbol TEXT NOT NULL, file TEXT NOT NULL, line INTEGER NOT NULL, col INTEGER NOT NULL,
 end_line INTEGER NOT NULL, end_col INTEGER NOT NULL, role INTEGER NOT NULL,
 content_hash TEXT NOT NULL
) STRICT;
CREATE INDEX scip_occurrence_symbol ON scip_occurrences(repository_id,symbol);
CREATE TABLE scip_relationships (
 workspace_id TEXT NOT NULL, repository_id TEXT NOT NULL REFERENCES repositories(repository_id) ON DELETE CASCADE,
 symbol TEXT NOT NULL, related TEXT NOT NULL, kind TEXT NOT NULL,
 PRIMARY KEY(repository_id,symbol,related,kind)
) STRICT;

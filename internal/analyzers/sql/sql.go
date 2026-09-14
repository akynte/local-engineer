package sql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads .sql files and emits the schema they define.
type Analyzer struct {
	MaxFileBytes int64
	Warnf        func(format string, args ...any)
	// Unparsed counts statements the parser deliberately did not interpret.
	// It is reported so the fraction of a schema the graph does not cover is
	// visible rather than invisible.
	Unparsed int
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 4 << 20} }

func (a *Analyzer) Name() string { return "sql" }

func (a *Analyzer) Handles(f index.File) bool { return f.Lang == "sql" }

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

// tableFQN namespaces a table so it cannot collide with a Go type of the same
// name.
func tableFQN(name string) string { return "table:" + strings.ToLower(name) }

func columnFQN(table, column string) string {
	return tableFQN(table) + "." + strings.ToLower(column)
}

// Analyze parses every SQL file and emits tables, columns and their
// relationships.
//
// Migrations are applied in file order, so a later ALTER TABLE adding a column
// is reflected in the table it alters. That ordering is what makes the result
// the schema as it ends up, rather than a pile of unrelated statements.
func (a *Analyzer) Analyze(ctx context.Context, repoRoot string, files []index.File) (index.Result, error) {
	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	// tables accumulates across files, so a column added by a later migration
	// lands on the table an earlier one created.
	type tableState struct {
		columns map[string]Column
		file    string
	}
	tables := map[string]*tableState{}
	var order []string
	var totalStatements int

	for _, f := range sorted {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if a.MaxFileBytes > 0 && f.Size > a.MaxFileBytes {
			continue
		}
		abs := f.AbsPath
		if abs == "" {
			abs = filepath.Join(repoRoot, filepath.FromSlash(f.Path))
		}
		body, err := os.ReadFile(abs) //nolint:gosec // a path the indexer walked inside the repository
		if err != nil {
			a.warn("sql: %s: %v", f.Path, err)
			continue
		}

		// The file itself is a schema object, so "which migration created
		// this" is one hop.
		schemaFQN := "schema:" + f.Path
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindSchema, Name: filepath.Base(f.Path), FQN: schemaFQN,
			Attrs: attrs(map[string]string{"file": f.Path}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindFile, SrcFQN: f.Path,
			DstKind: graph.KindSchema, DstFQN: schemaFQN,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})

		for _, st := range Parse(string(body)) {
			totalStatements++
			if st.Kind == Unparsed {
				a.Unparsed++
				continue
			}
			key := strings.ToLower(st.Table)

			switch st.Kind {
			case CreateTable:
				if _, seen := tables[key]; !seen {
					tables[key] = &tableState{columns: map[string]Column{}, file: f.Path}
					order = append(order, key)
				}
				tables[key].file = f.Path
				for _, c := range st.Columns {
					tables[key].columns[strings.ToLower(c.Name)] = c
				}

			case AlterTable:
				if _, seen := tables[key]; !seen {
					// An ALTER on a table this analyzer never saw created: the
					// table exists, it was just defined elsewhere.
					tables[key] = &tableState{columns: map[string]Column{}, file: f.Path}
					order = append(order, key)
				}
				for _, c := range st.Columns {
					tables[key].columns[strings.ToLower(c.Name)] = c
				}
				for _, dropped := range st.DroppedColumns {
					delete(tables[key].columns, strings.ToLower(dropped))
				}

			case DropTable:
				delete(tables, key)

			case CreateIndex:
				if st.Table == "" {
					continue
				}
				name := st.Index
				if name == "" {
					name = "(unnamed)"
				}
				idxFQN := "index:" + strings.ToLower(name)
				res.Nodes = append(res.Nodes, graph.Node{
					Kind: graph.KindInfraResource, Name: name, FQN: idxFQN,
					Attrs: attrs(map[string]string{"kind": "index", "table": st.Table,
						"columns": strings.Join(st.IndexColumns, ",")}),
				})
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindInfraResource, SrcFQN: idxFQN,
					DstKind: graph.KindTable, DstFQN: tableFQN(st.Table),
					Kind: graph.EdgeReadsSchema, Evidence: graph.Resolved,
				})
			}

			// A migration writes the schema objects it touches.
			if st.Table != "" && (st.Kind == CreateTable || st.Kind == AlterTable || st.Kind == DropTable) {
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindSchema, SrcFQN: schemaFQN,
					DstKind: graph.KindTable, DstFQN: tableFQN(st.Table),
					Kind: graph.EdgeWritesSchema, Evidence: graph.Resolved,
					Attrs: attrs(map[string]string{"statement": string(st.Kind)}),
				})
			}
			for _, ref := range st.References {
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindTable, SrcFQN: tableFQN(st.Table),
					DstKind: graph.KindTable, DstFQN: tableFQN(ref),
					Kind: graph.EdgeDependsOn, Evidence: graph.Resolved,
					Attrs: attrs(map[string]string{"constraint": "foreign_key"}),
				})
			}
		}
	}

	// Emit the schema as it ends up, after every migration has been applied.
	for _, key := range order {
		state, alive := tables[key]
		if !alive {
			continue // dropped by a later migration
		}
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindTable, Name: key, FQN: tableFQN(key),
			Attrs: attrs(map[string]string{"defined_in": state.file,
				"columns": fmt.Sprint(len(state.columns))}),
		})

		names := make([]string, 0, len(state.columns))
		for name := range state.columns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			c := state.columns[name]
			res.Nodes = append(res.Nodes, graph.Node{
				Kind: graph.KindColumn, Name: c.Name, FQN: columnFQN(key, name),
				Signature: c.Type,
				Attrs: attrs(map[string]string{
					"type": c.Type, "not_null": boolStr(c.NotNull),
					"primary_key": boolStr(c.PrimaryKey), "has_default": boolStr(c.HasDefault),
				}),
			})
			res.Edges = append(res.Edges, index.PendingEdge{
				SrcKind: graph.KindTable, SrcFQN: tableFQN(key),
				DstKind: graph.KindColumn, DstFQN: columnFQN(key, name),
				Kind: graph.EdgeContains, Evidence: graph.Resolved,
			})
		}
	}

	if a.Unparsed > 0 {
		// Saying how much was not understood is the difference between a
		// graph with known gaps and one that quietly describes a schema that
		// is not there.
		a.warn("sql: %d of %d statements were not parsed (the DDL subset covers "+
			"CREATE/ALTER/DROP TABLE, CREATE INDEX and CREATE VIEW)", a.Unparsed, totalStatements)
	}
	return res, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return ""
}

func attrs(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k, v := range kv {
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "{}"
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%q", k, kv[k])
	}
	b.WriteByte('}')
	return b.String()
}

// TableFQN exposes the naming convention so other analyzers can point at the
// same nodes.
func TableFQN(name string) string { return tableFQN(name) }

// Package scipindex imports compiler-produced SCIP cross references into the
// workspace index. It retains exact occurrences alongside the shared graph.
package scipindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/worktree"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type Stats struct {
	ExcludedDocuments int `json:"excluded_documents"`
	Documents         int `json:"documents"`
	Symbols           int `json:"symbols"`
	Occurrences       int `json:"occurrences"`
	Relationships     int `json:"relationships"`
}
type source struct {
	doc            *scip.Document
	hash           string
	fileID, nodeID int64
}
type symbol struct {
	info       *scip.SymbolInformation
	src        *source
	start, end int
	nodeID     int64
	kind       string
}

func scoped(file, name string) string {
	if strings.HasPrefix(name, "local ") {
		return file + "::" + name
	}
	return name
}
func kindOf(info *scip.SymbolInformation) string {
	switch strings.ToLower(info.Kind.String()) {
	case "function", "method", "class", "interface", "field", "constant", "variable", "module", "type":
		return strings.ToLower(info.Kind.String())
	case "struct", "enum", "typealias":
		return "type"
	}
	return "variable"
}

// Import replaces the repository's previous SCIP import atomically. Files must
// already be indexed; secret paths, escaping paths and mismatched embedded
// source text are refused. The observed source hashes support invalidation.
func Import(ctx context.Context, st *store.Store, repoID, root, indexPath string) (Stats, error) {
	var stats Stats
	info, err := os.Stat(indexPath)
	if err != nil {
		return stats, err
	}
	if info.Size() > 256<<20 {
		return stats, fmt.Errorf("SCIP index exceeds 256 MiB")
	}
	body, err := os.ReadFile(indexPath)
	if err != nil {
		return stats, err
	}
	var idx scip.Index
	if err := proto.Unmarshal(body, &idx); err != nil {
		return stats, fmt.Errorf("decode SCIP: %w", err)
	}
	var sources []*source
	symbols := map[string]*symbol{}
	for _, doc := range idx.Documents {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		file := doc.RelativePath
		if path.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") {
			stats.ExcludedDocuments++
			continue
		}
		if file == "" || path.Clean(file) != file || strings.Contains(file, "\\") {
			return stats, fmt.Errorf("invalid SCIP document path %q", file)
		}
		if err := (firewall.Access{}).Check(root, file, false); err != nil {
			return stats, err
		}
		full, err := worktree.Resolve(root, file)
		if err != nil {
			return stats, err
		}
		text, err := os.ReadFile(full)
		if err != nil {
			return stats, err
		}
		if doc.Text != "" && doc.Text != string(text) {
			return stats, fmt.Errorf("SCIP source differs from checkout: %s", file)
		}
		h := sha256.Sum256(text)
		src := &source{doc: doc, hash: hex.EncodeToString(h[:])}
		if err := st.Index().SQL().QueryRowContext(ctx, `SELECT file_id FROM files WHERE workspace_id=? AND repository_id=? AND path=?`, st.ID().String(), repoID, file).Scan(&src.fileID); err != nil {
			return stats, fmt.Errorf("index source file %s before importing SCIP: %w", file, err)
		}
		if err := st.Index().SQL().QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE repository_id=? AND file_id=? AND kind='file'`, repoID, src.fileID).Scan(&src.nodeID); err != nil {
			return stats, err
		}
		sources = append(sources, src)
		for _, info := range doc.Symbols {
			name := scoped(file, info.Symbol)
			if _, exists := symbols[name]; exists {
				continue
			}
			symbols[name] = &symbol{info: info, src: src, kind: kindOf(info)}
		}
		for _, occ := range doc.Occurrences {
			r, ok := occ.SourceRange()
			if !ok || r.Start.Line < 0 || r.Start.Character < 0 || r.End.Line < r.Start.Line {
				return stats, fmt.Errorf("invalid SCIP occurrence range in %s", file)
			}
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
				name := scoped(file, occ.Symbol)
				sym := symbols[name]
				if sym == nil {
					sym = &symbol{info: &scip.SymbolInformation{Symbol: occ.Symbol, DisplayName: occ.Symbol}, src: src, kind: "variable"}
					symbols[name] = sym
				}
				if sym.src != src {
					continue
				}
				sym.start, sym.end = int(r.Start.Line)+1, int(r.End.Line)+1
				if enclosing, ok := occ.EnclosingSourceRange(); ok {
					sym.start, sym.end = int(enclosing.Start.Line)+1, int(enclosing.End.Line)+1
				}
			}
		}
	}
	err = st.Index().Tx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"scip_occurrences", "scip_relationships", "scip_symbols"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE repository_id=?`, repoID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE repository_id=? AND json_extract(attrs,'$.source')='scip'`, repoID); err != nil {
			return err
		}
		for name, sym := range symbols {
			signature := sym.info.GetSignatureDocumentation().GetText()
			display := sym.info.DisplayName
			if display == "" {
				display = name
			}
			docs, _ := json.Marshal(sym.info.Documentation)
			res, err := tx.ExecContext(ctx, `INSERT INTO nodes(workspace_id,repository_id,kind,name,fqn,file_id,start_line,end_line,signature,content_hash,attrs,index_version) VALUES(?,?,?,?,?,?,?,?,?,?,'{"source":"scip"}',?)`, st.ID().String(), repoID, sym.kind, display, name, sym.src.fileID, sym.start, sym.end, signature, sym.src.hash, version.IndexerVersion)
			if err != nil {
				return err
			}
			sym.nodeID, err = res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO scip_symbols VALUES(?,?,?,?,?,?,?,?,?,?,?)`, st.ID().String(), repoID, name, display, sym.kind, sym.src.doc.RelativePath, sym.start, signature, string(docs), sym.src.hash, sym.nodeID); err != nil {
				return err
			}
			stats.Symbols++
		}
		edge := func(src, dst int64, kind string) error {
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO edges(workspace_id,src_id,dst_id,kind,evidence,source,index_version) VALUES(?,?,?,?,'resolved','scip',?)`, st.ID().String(), src, dst, kind, version.IndexerVersion)
			return err
		}
		for _, src := range sources {
			file := src.doc.RelativePath
			stats.Documents++
			for _, occ := range src.doc.Occurrences {
				r, _ := occ.SourceRange()
				name := scoped(file, occ.Symbol)
				if _, err := tx.ExecContext(ctx, `INSERT INTO scip_occurrences VALUES(?,?,?,?,?,?,?,?,?,?)`, st.ID().String(), repoID, name, file, r.Start.Line+1, r.Start.Character, r.End.Line+1, r.End.Character, occ.SymbolRoles, src.hash); err != nil {
					return err
				}
				stats.Occurrences++
				if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
					continue
				}
				target := symbols[name]
				if target == nil {
					continue
				}
				ownerID := src.nodeID
				span := int(^uint(0) >> 1)
				for _, owner := range symbols {
					if owner.src == src && owner.nodeID != target.nodeID && owner.start <= int(r.Start.Line)+1 && owner.end >= int(r.End.Line)+1 && owner.end-owner.start < span {
						ownerID = owner.nodeID
						span = owner.end - owner.start
					}
				}
				if err := edge(ownerID, target.nodeID, "references"); err != nil {
					return err
				}
			}
		}
		for name, sym := range symbols {
			for _, rel := range sym.info.Relationships {
				other := scoped(sym.src.doc.RelativePath, rel.Symbol)
				for _, link := range []struct {
					active bool
					kind   string
				}{{rel.IsImplementation, "implements"}, {rel.IsReference, "references"}, {rel.IsTypeDefinition, "uses_type"}, {rel.IsDefinition, "references"}} {
					if !link.active {
						continue
					}
					if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO scip_relationships VALUES(?,?,?,?,?)`, st.ID().String(), repoID, name, other, link.kind); err != nil {
						return err
					}
					stats.Relationships++
					if target := symbols[other]; target != nil {
						if err := edge(sym.nodeID, target.nodeID, link.kind); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
	return stats, err
}

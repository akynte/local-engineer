// Package protoavro contributes the proto and Avro half of design v3 §3.2's
// "schema to application code" row.
//
// Protobuf and Avro define a contract in a file and generate code from it, so
// the interesting relationship is not inside the generated code — it is between
// the schema file and the types the rest of the repository uses. A field
// removed from a .proto breaks every consumer of the generated struct, and
// without these nodes the graph cannot name one of them.
//
// What this does NOT do is parse the full grammars. It reads declarations:
// messages, enums, services and their RPCs from proto; records, fields and
// their types from Avro's JSON. That is enough to name the contract and link it
// to what it generates, and it is honest about being a reading rather than a
// compilation — every edge here is `declared`, because the file states these
// things outright and whether the generated code matches is a question about
// the generator having been run.
package protoavro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

// Analyzer reads .proto and Avro schema files.
type Analyzer struct {
	MaxFileBytes int64
	Warnf        func(format string, args ...any)
}

// New returns an analyzer with the shipped defaults.
func New() *Analyzer { return &Analyzer{MaxFileBytes: 2 << 20} }

func (a *Analyzer) Name() string { return "protoavro" }

func (a *Analyzer) Handles(f index.File) bool {
	switch strings.ToLower(filepath.Ext(f.Path)) {
	case ".proto", ".avsc":
		return true
	case ".avro":
		return true
	}
	return false
}

func (a *Analyzer) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
	}
}

func schemaFQN(rel string) string { return "schema:" + filepath.ToSlash(rel) }
func messageFQN(pkg, name string) string {
	if pkg == "" {
		return "proto:" + name
	}
	return "proto:" + pkg + "." + name
}
func recordFQN(ns, name string) string {
	if ns == "" {
		return "avro:" + name
	}
	return "avro:" + ns + "." + name
}

// Analyze reads every accepted file.
func (a *Analyzer) Analyze(_ context.Context, repoRoot string, files []index.File) (index.Result, error) {
	var res index.Result

	sorted := append([]index.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		body, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f.Path)))
		if err != nil {
			a.warn("protoavro: %s: %v", f.Path, err)
			continue
		}
		if a.MaxFileBytes > 0 && int64(len(body)) > a.MaxFileBytes {
			a.warn("protoavro: %s: skipped, %d bytes exceeds the limit", f.Path, len(body))
			continue
		}
		rel := filepath.ToSlash(f.Path)
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindSchema, Name: filepath.Base(rel), FQN: schemaFQN(rel), Path: rel,
		})
		if strings.HasSuffix(strings.ToLower(rel), ".proto") {
			a.proto(&res, rel, string(body))
		} else {
			a.avro(&res, rel, body)
		}
	}
	return res, nil
}

var (
	protoPackage = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)
	protoMessage = regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	protoEnum    = regexp.MustCompile(`(?m)^\s*enum\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	protoService = regexp.MustCompile(`(?m)^\s*service\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)
	protoRPC     = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*([A-Za-z0-9_.]+)\s*\)\s*returns\s*\(\s*([A-Za-z0-9_.]+)\s*\)`)
	protoImport  = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"\s*;`)
)

// proto reads declarations from a .proto file. Comments are stripped first so
// a commented-out message does not become a node.
func (a *Analyzer) proto(res *index.Result, rel, src string) {
	src = stripComments(src)

	pkg := ""
	if m := protoPackage.FindStringSubmatch(src); m != nil {
		pkg = m[1]
	}

	add := func(kind graph.NodeKind, name, fqn string) {
		res.Nodes = append(res.Nodes, graph.Node{Kind: kind, Name: name, FQN: fqn, Path: rel})
		// The schema file contains the declaration.
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindSchema, SrcFQN: schemaFQN(rel),
			DstKind: kind, DstFQN: fqn,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})
	}

	for _, m := range protoMessage.FindAllStringSubmatch(src, -1) {
		add(graph.KindType, m[1], messageFQN(pkg, m[1]))
	}
	for _, m := range protoEnum.FindAllStringSubmatch(src, -1) {
		add(graph.KindType, m[1], messageFQN(pkg, m[1]))
	}
	// Services, and the RPCs inside each one. The RPCs have to be found within
	// the service's own body: a file-wide scan would attribute every RPC to
	// whichever service happened to be declared first.
	for _, loc := range protoService.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		serviceFQN := messageFQN(pkg, name)
		add(graph.KindAPI, name, serviceFQN)

		body, ok := blockAfter(src, loc[1]-1)
		if !ok {
			continue
		}
		// An RPC consumes its request and response messages, so the service
		// points at them: changing a message must find the RPCs that carry it,
		// and impact analysis is a reverse traversal.
		for _, m := range protoRPC.FindAllStringSubmatch(body, -1) {
			for _, msg := range []string{m[2], m[3]} {
				res.Edges = append(res.Edges, index.PendingEdge{
					SrcKind: graph.KindAPI, SrcFQN: serviceFQN,
					DstKind: graph.KindType, DstFQN: qualify(pkg, msg),
					Kind: graph.EdgeUsesType, Evidence: graph.Declared,
					Attrs: attrs(map[string]string{"rpc": m[1]}),
				})
			}
		}
	}

	// An import is a dependency between contracts.
	for _, m := range protoImport.FindAllStringSubmatch(src, -1) {
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindSchema, SrcFQN: schemaFQN(rel),
			DstKind: graph.KindSchema, DstFQN: schemaFQN(m[1]),
			Kind: graph.EdgeImports, Evidence: graph.Declared,
		})
	}
}

// blockAfter returns the text between the brace at or after start and its
// matching close. Nested braces are counted, so a message declared inside a
// service does not end the block early.
func blockAfter(src string, start int) (string, bool) {
	open := strings.IndexByte(src[start:], '{')
	if open < 0 {
		return "", false
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

// qualify resolves a possibly-unqualified message reference against the file's
// package. A name that already carries a dot is taken as fully qualified.
func qualify(pkg, name string) string {
	if strings.Contains(name, ".") {
		return "proto:" + name
	}
	return messageFQN(pkg, name)
}

// avroSchema is the subset of Avro's JSON this reads.
type avroSchema struct {
	Type      string      `json:"type"`
	Name      string      `json:"name"`
	Namespace string      `json:"namespace"`
	Fields    []avroField `json:"fields"`
	Symbols   []string    `json:"symbols"`
}

type avroField struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

// avro reads an Avro schema. Avro is JSON, so this is a parse rather than a
// pattern match — but a union type is a nested array of anything, and chasing
// every shape would be a schema compiler. Field types are recorded as text.
func (a *Analyzer) avro(res *index.Result, rel string, body []byte) {
	var s avroSchema
	if err := json.Unmarshal(body, &s); err != nil {
		a.warn("protoavro: %s is not readable Avro JSON: %v", rel, err)
		return
	}
	if s.Name == "" {
		a.warn("protoavro: %s declares no name", rel)
		return
	}
	fqn := recordFQN(s.Namespace, s.Name)
	res.Nodes = append(res.Nodes, graph.Node{
		Kind: graph.KindType, Name: s.Name, FQN: fqn, Path: rel,
		Attrs: attrs(map[string]string{"avro_type": s.Type, "namespace": s.Namespace}),
	})
	res.Edges = append(res.Edges, index.PendingEdge{
		SrcKind: graph.KindSchema, SrcFQN: schemaFQN(rel),
		DstKind: graph.KindType, DstFQN: fqn,
		Kind: graph.EdgeContains, Evidence: graph.Resolved,
	})

	for _, f := range s.Fields {
		fieldFQN := fqn + "." + f.Name
		res.Nodes = append(res.Nodes, graph.Node{
			Kind: graph.KindField, Name: f.Name, FQN: fieldFQN, Path: rel,
			Attrs: attrs(map[string]string{"type": strings.TrimSpace(string(f.Type))}),
		})
		res.Edges = append(res.Edges, index.PendingEdge{
			SrcKind: graph.KindType, SrcFQN: fqn,
			DstKind: graph.KindField, DstFQN: fieldFQN,
			Kind: graph.EdgeContains, Evidence: graph.Resolved,
		})
	}
}

// stripComments blanks // and /* */ comments, preserving offsets so line
// numbers stay usable. A commented-out message must not become a node.
func stripComments(src string) string {
	out := []byte(src)
	const (
		code = iota
		line
		block
		str
	)
	state := code
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				state = line
				out[i], out[i+1] = ' ', ' '
				i++
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				state = block
				out[i], out[i+1] = ' ', ' '
				i++
			case c == '"':
				state = str
			}
		case line:
			if c == '\n' {
				state = code
			} else {
				out[i] = ' '
			}
		case block:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' {
				out[i] = ' '
			}
		case str:
			if c == '\\' && i+1 < len(out) {
				i++
				continue
			}
			if c == '"' || c == '\n' {
				state = code
			}
		}
	}
	return string(out)
}

func attrs(m map[string]string) string {
	body, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(body)
}

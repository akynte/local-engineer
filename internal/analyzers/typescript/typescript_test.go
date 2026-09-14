package typescript

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
)

func writeTree(t *testing.T, files map[string]string) (string, []index.File) {
	t.Helper()
	root := t.TempDir()
	var list []index.File
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		list = append(list, index.File{Path: rel})
	}
	return root, list
}

func analyze(t *testing.T, files map[string]string) index.Result {
	t.Helper()
	root, list := writeTree(t, files)
	a := New()
	a.Warnf = func(f string, args ...any) { t.Logf("warn: "+f, args...) }
	var accepted []index.File
	for _, f := range list {
		if a.Handles(f) {
			accepted = append(accepted, f)
		}
	}
	res, err := a.Analyze(context.Background(), root, accepted)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func hasEdge(res index.Result, srcFQN, dstFQN string, kind graph.EdgeKind, ev graph.Evidence) bool {
	for _, e := range res.Edges {
		if e.SrcFQN == srcFQN && e.DstFQN == dstFQN && e.Kind == kind && e.Evidence == ev {
			return true
		}
	}
	return false
}

// A relative import that resolves to a file on disk is a filesystem fact, so it
// is `resolved`. The edge points importer to imported, because impact analysis
// is a reverse traversal: changing the imported module must find its importers.
func TestRelativeImportIsResolved(t *testing.T) {
	res := analyze(t, map[string]string{
		"src/app.ts":         "import { User } from './models/user';\nexport function main() { return User; }\n",
		"src/models/user.ts": "export interface User { id: string }\n",
	})
	if !hasEdge(res, "ts:src/app.ts", "ts:src/models/user.ts", graph.EdgeImports, graph.Resolved) {
		t.Fatalf("no resolved import edge from app.ts to user.ts:\n%+v", res.Edges)
	}
}

// Extensionless, index and .js-means-.ts resolution are all part of the rules.
func TestResolutionRules(t *testing.T) {
	res := analyze(t, map[string]string{
		"a.ts":         "import './dir';\nimport './other.js';\n",
		"dir/index.ts": "export const x = 1;\n",
		"other.ts":     "export const y = 2;\n",
	})
	if !hasEdge(res, "ts:a.ts", "ts:dir/index.ts", graph.EdgeImports, graph.Resolved) {
		t.Error("directory import did not resolve to index.ts")
	}
	if !hasEdge(res, "ts:a.ts", "ts:other.ts", graph.EdgeImports, graph.Resolved) {
		t.Error("./other.js did not resolve to other.ts")
	}
}

// A bare specifier is a dependency the manifest declares, not a file this
// analyzer can verify, so it is `declared` and scoped to the package root.
func TestPackageImportIsDeclared(t *testing.T) {
	res := analyze(t, map[string]string{
		"a.ts": "import React from 'react';\nimport { z } from '@scope/pkg/sub/deep';\n",
	})
	if !hasEdge(res, "ts:a.ts", "npm:react", graph.EdgeDependsOn, graph.Declared) {
		t.Error("no declared dependency edge to react")
	}
	if !hasEdge(res, "ts:a.ts", "npm:@scope/pkg", graph.EdgeDependsOn, graph.Declared) {
		t.Errorf("a scoped subpath import was not reduced to its package root:\n%+v", res.Edges)
	}
}

// implements points the implementing class at the interface, the same direction
// the Go analyzer uses, so adding a method to an interface finds the types that
// must change.
func TestImplementsPointsClassToInterface(t *testing.T) {
	res := analyze(t, map[string]string{
		"repo.ts": "export interface Repo { get(): string }\n" +
			"export class SqlRepo implements Repo { get() { return 'x'; } }\n",
	})
	if !hasEdge(res, "ts:repo.ts#SqlRepo", "ts:repo.ts#Repo", graph.EdgeImplements, graph.Declared) {
		t.Fatalf("implements edge missing or reversed:\n%+v", res.Edges)
	}
}

func TestExtendsIsRecorded(t *testing.T) {
	res := analyze(t, map[string]string{
		"b.ts": "export class Base {}\nexport class Derived extends Base {}\n",
	})
	if !hasEdge(res, "ts:b.ts#Derived", "ts:b.ts#Base", graph.EdgeExtends, graph.Declared) {
		t.Fatalf("extends edge missing:\n%+v", res.Edges)
	}
}

// An ambiguous name is not an edge. Two exported types with the same name in
// different modules are common, and guessing is wrong half the time.
func TestAmbiguousHeritageIsNotAnEdge(t *testing.T) {
	res := analyze(t, map[string]string{
		"one.ts": "export interface Handler { run(): void }\n",
		"two.ts": "export interface Handler { run(): void }\n",
		"use.ts": "export class Impl implements Handler { run() {} }\n",
	})
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeImplements {
			t.Fatalf("an ambiguous interface name produced an edge: %+v", e)
		}
	}
}

// process.env is two files agreeing on a string, so it is `inferred`.
func TestEnvReadsAreInferred(t *testing.T) {
	res := analyze(t, map[string]string{
		"cfg.ts": "export const url = process.env.DATABASE_URL;\n" +
			"export const port = process.env['PORT'];\n",
	})
	if !hasEdge(res, "ts:cfg.ts", "config:DATABASE_URL", graph.EdgeReadsConfig, graph.Inferred) {
		t.Error("no inferred config edge for DATABASE_URL")
	}
	if !hasEdge(res, "ts:cfg.ts", "config:PORT", graph.EdgeReadsConfig, graph.Inferred) {
		t.Errorf("bracket form of process.env was not read:\n%+v", res.Edges)
	}
}

// Comments and strings must not produce edges. An analyzer that reports an
// import because the word appeared in a doc comment is worse than one that
// reports nothing.
func TestCommentsAndStringsAreIgnored(t *testing.T) {
	res := analyze(t, map[string]string{
		"c.ts": "// import Real from 'react';\n" +
			"/* import Other from 'vue';\n   still a comment */\n" +
			"const s = \"import Fake from 'angular'\";\n" +
			"const t = `import Template from 'svelte'`;\n" +
			"export const ok = 1;\n",
	})
	for _, n := range res.Nodes {
		if n.Kind == graph.KindDependency {
			t.Errorf("a commented-out or quoted import produced a dependency node: %s", n.FQN)
		}
	}
}

// A regular expression containing a quote must not swallow the rest of the file.
func TestRegexLiteralDoesNotBreakScanning(t *testing.T) {
	res := analyze(t, map[string]string{
		"r.ts": "const re = /it's a trap/g;\nimport React from 'react';\nexport const after = 1;\n",
	})
	if !hasEdge(res, "ts:r.ts", "npm:react", graph.EdgeDependsOn, graph.Declared) {
		t.Errorf("an apostrophe inside a regex hid the rest of the file:\n%+v", res.Edges)
	}
	var found bool
	for _, n := range res.Nodes {
		if n.FQN == "ts:r.ts#after" {
			found = true
		}
	}
	if !found {
		t.Error("the declaration after the regex was not seen")
	}
}

func TestExportFromIsAnImport(t *testing.T) {
	res := analyze(t, map[string]string{
		"index.ts": "export { User } from './user';\nexport * from './other';\n",
		"user.ts":  "export interface User { id: string }\n",
		"other.ts": "export const z = 1;\n",
	})
	if !hasEdge(res, "ts:index.ts", "ts:user.ts", graph.EdgeImports, graph.Resolved) {
		t.Error("export-from did not produce an import edge")
	}
	if !hasEdge(res, "ts:index.ts", "ts:other.ts", graph.EdgeImports, graph.Resolved) {
		t.Error("export-star did not produce an import edge")
	}
}

func TestDynamicImportAndRequire(t *testing.T) {
	res := analyze(t, map[string]string{
		"d.ts":    "async function load() { return import('./lazy'); }\nconst fs = require('node:fs');\n",
		"lazy.ts": "export const v = 1;\n",
	})
	if !hasEdge(res, "ts:d.ts", "ts:lazy.ts", graph.EdgeImports, graph.Resolved) {
		t.Errorf("dynamic import was not resolved:\n%+v", res.Edges)
	}
	if !hasEdge(res, "ts:d.ts", "npm:node:fs", graph.EdgeDependsOn, graph.Declared) {
		t.Errorf("require was not recorded:\n%+v", res.Edges)
	}
}

// Minified bundles are cost without retrieval value.
func TestHandlesSkipsBundles(t *testing.T) {
	a := New()
	for _, path := range []string{"dist/app.min.js", "dist/vendor.bundle.js"} {
		if a.Handles(index.File{Path: path}) {
			t.Errorf("%s was accepted", path)
		}
	}
	for _, path := range []string{"src/a.ts", "src/b.tsx", "src/c.mts"} {
		if !a.Handles(index.File{Path: path}) {
			t.Errorf("%s was rejected", path)
		}
	}
}

// Declarations are contained by their module, and exportedness is recorded so a
// consumer of the graph can tell what can cross a module boundary.
func TestDeclarationsAreContainedAndScoped(t *testing.T) {
	res := analyze(t, map[string]string{
		"s.ts": "export function pub() {}\nfunction priv() {}\n",
	})
	if !hasEdge(res, "ts:s.ts", "ts:s.ts#pub", graph.EdgeContains, graph.Resolved) {
		t.Error("module does not contain its exported function")
	}
	for _, n := range res.Nodes {
		switch n.FQN {
		case "ts:s.ts#pub":
			if n.Visibility != "exported" {
				t.Errorf("pub visibility = %q, want exported", n.Visibility)
			}
		case "ts:s.ts#priv":
			if n.Visibility != "unexported" {
				t.Errorf("priv visibility = %q, want unexported", n.Visibility)
			}
		}
	}
}

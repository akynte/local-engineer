'use strict';

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { analyze } = require('../analyze.js');

function fixture(files) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ts-sidecar-'));
  for (const [rel, body] of Object.entries(files)) {
    const p = path.join(dir, rel);
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, body);
  }
  return dir;
}

const hasEdge = (res, src, dst, kind, evidence) =>
  res.edges.some((e) =>
    e.src_fqn === src && e.dst_fqn === dst && e.kind === kind &&
    (evidence === undefined || e.evidence === evidence));

// The whole reason the sidecar exists: the lexical analyzer deliberately emits
// no call graph, because without a checker an identifier in call position may
// be a local or a shadowed binding. With a checker, it is answerable.
test('call sites resolve to the declaration actually reached', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({ compilerOptions: { strict: true }, include: ['src'] }),
    'src/util.ts': 'export function helper(): number { return 1; }\n',
    'src/app.ts':
      "import { helper } from './util';\n" +
      'export function run(): number { return helper(); }\n',
  });
  const res = analyze(dir);
  assert.ok(
    hasEdge(res, 'ts:src/app.ts#run', 'ts:src/util.ts#helper', 'calls', 'resolved'),
    'no resolved call edge:\n' + JSON.stringify(res.edges, null, 1));
});

// A local shadowing an imported name must not produce an edge to the import.
// This is exactly the false positive the lexical analyzer refuses to risk.
test('a shadowing local does not produce a false call edge', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({ include: ['src'] }),
    'src/util.ts': 'export function helper(): number { return 1; }\n',
    'src/app.ts':
      "import { helper as imported } from './util';\n" +
      'export function run(): number {\n' +
      '  const helper = () => 2;\n' +
      '  return helper();\n' +
      '}\n',
  });
  const res = analyze(dir);
  assert.ok(
    !hasEdge(res, 'ts:src/app.ts#run', 'ts:src/util.ts#helper', 'calls'),
    'a shadowed local was resolved to the import it shadows');
});

// implements points the class at the interface, matching the Go analyzer and
// the direction invariant: impact analysis is a reverse traversal.
test('implements points the class at the interface', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({ include: ['src'] }),
    'src/store.ts':
      'export interface Store { load(id: string): string }\n' +
      'export class SqlStore implements Store { load(id: string) { return id; } }\n',
  });
  const res = analyze(dir);
  assert.ok(
    hasEdge(res, 'ts:src/store.ts#SqlStore', 'ts:src/store.ts#Store', 'implements', 'resolved'),
    'implements edge missing or reversed:\n' + JSON.stringify(res.edges, null, 1));
});

// tsconfig path aliases are the thing the lexical analyzer cannot follow.
test('tsconfig path aliases resolve', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({
      compilerOptions: { baseUrl: '.', paths: { '@lib/*': ['src/lib/*'] } },
      include: ['src'],
    }),
    'src/lib/math.ts': 'export function add(a: number, b: number): number { return a + b; }\n',
    'src/app.ts':
      "import { add } from '@lib/math';\n" +
      'export function run(): number { return add(1, 2); }\n',
  });
  const res = analyze(dir);
  assert.ok(
    hasEdge(res, 'ts:src/app.ts', 'ts:src/lib/math.ts', 'imports', 'resolved'),
    'an aliased import did not resolve:\n' + JSON.stringify(res.edges, null, 1));
});

// FQNs must match the Go analyzer's scheme, or the same thing gets two
// identities and the graph doubles up.
test('fqns match the Go analyzer scheme', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({ include: ['src'] }),
    'src/a.ts': 'export function f(): void {}\n',
  });
  const res = analyze(dir);
  assert.ok(res.nodes.some((n) => n.fqn === 'ts:src/a.ts'), 'module fqn');
  assert.ok(res.nodes.some((n) => n.fqn === 'ts:src/a.ts#f'), 'symbol fqn');
});

// An empty result with diagnostics means the analysis did not happen; an empty
// result without them means the repository has no TypeScript. A caller must be
// able to tell those apart.
test('a repository with no typescript is empty and quiet', () => {
  const dir = fixture({ 'README.md': '# nothing here\n' });
  const res = analyze(dir);
  assert.strictEqual(res.nodes.length, 0);
  assert.ok(!res.diagnostics.some((d) => d.level === 'error'),
    'an empty repository reported an error: ' + JSON.stringify(res.diagnostics));
});

test('a broken tsconfig is reported rather than silently empty', () => {
  const dir = fixture({ 'tsconfig.json': '{ this is not json' });
  const res = analyze(dir);
  assert.ok(res.diagnostics.some((d) => d.level === 'error'),
    'an unreadable tsconfig produced no diagnostic');
});

// Bare specifiers are a manifest claim, not a file this can verify.
test('package imports are declared, not resolved', () => {
  const dir = fixture({
    'tsconfig.json': JSON.stringify({ include: ['src'] }),
    'src/a.ts': "import * as React from 'react';\nexport const x = React;\n",
  });
  const res = analyze(dir);
  assert.ok(
    hasEdge(res, 'ts:src/a.ts', 'npm:react', 'depends_on', 'declared'),
    'no declared dependency edge:\n' + JSON.stringify(res.edges, null, 1));
});

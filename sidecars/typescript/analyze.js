// Type-checked TypeScript analysis for local-engineer (design v3 §3.2).
//
// The Go analyzer reads the source and is honest that reading is all it does.
// This has the compiler, so it can answer the questions reading cannot: which
// symbol a call actually reaches, which interface a class actually implements,
// where a type is actually used. Those edges are `resolved`; the Go analyzer's
// are `declared` or `inferred`, and the difference is the point of §3.2
// carrying a category per relationship.
//
// Usage: node analyze.js <repo-root>
'use strict';

const path = require('path');
const fs = require('fs');
const ts = require('typescript');

/** Evidence categories, matching internal/graph. */
const RESOLVED = 'resolved';
const DECLARED = 'declared';

/**
 * FQNs must match the Go analyzer's scheme exactly, or the same thing gets two
 * identities and the graph doubles up.
 */
const moduleFQN = (rel) => 'ts:' + rel.split(path.sep).join('/');
const symbolFQN = (rel, name) => moduleFQN(rel) + '#' + name;

function main(argv) {
  const root = argv[2];
  if (!root) {
    process.stderr.write('usage: node analyze.js <repo-root>\n');
    return 2;
  }
  const out = analyze(path.resolve(root));
  process.stdout.write(JSON.stringify(out) + '\n');
  return 0;
}

function analyze(root) {
  const nodes = [];
  const edges = [];
  const diagnostics = [];

  const configPath = ts.findConfigFile(root, ts.sys.fileExists, 'tsconfig.json');
  let fileNames;
  let options;

  if (configPath) {
    const parsed = readConfig(configPath, root, diagnostics);
    if (!parsed) return { nodes, edges, diagnostics };
    fileNames = parsed.fileNames;
    options = parsed.options;
  } else {
    // No tsconfig is not a failure — plenty of repositories have TypeScript
    // without one — but it is worth saying, because resolution then uses
    // defaults rather than the project's own paths.
    diagnostics.push({
      level: 'info',
      message: 'no tsconfig.json found; using default compiler options, so path ' +
        'aliases configured in the project will not resolve',
    });
    fileNames = collectSources(root);
    options = { allowJs: true, target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS };
  }

  if (fileNames.length === 0) {
    return { nodes, edges, diagnostics };
  }

  const program = ts.createProgram(fileNames, options);
  const checker = program.getTypeChecker();

  const rel = (f) => path.relative(root, f).split(path.sep).join('/');
  const isLocal = (f) => {
    const r = path.relative(root, f);
    return r !== '' && !r.startsWith('..') && !r.includes('node_modules');
  };

  for (const sf of program.getSourceFiles()) {
    if (sf.isDeclarationFile || !isLocal(sf.fileName)) continue;
    const r = rel(sf.fileName);

    nodes.push({ kind: 'module', name: path.basename(r), fqn: moduleFQN(r), path: r });

    walk(sf, sf, r);

    /** Recursive walk, carrying the enclosing declaration for call edges. */
    function walk(node, sourceFile, relPath, enclosing) {
      if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) {
        emitModuleEdge(node, sourceFile, relPath);
      }

      if (ts.isFunctionDeclaration(node) && node.name) {
        const name = node.name.getText(sourceFile);
        addDecl(relPath, name, 'function', node, sourceFile);
        enclosing = { fqn: symbolFQN(relPath, name), kind: 'function' };
      } else if (ts.isClassDeclaration(node) && node.name) {
        const name = node.name.getText(sourceFile);
        addDecl(relPath, name, 'class', node, sourceFile);
        emitHeritage(node, relPath, name, sourceFile);
        enclosing = { fqn: symbolFQN(relPath, name), kind: 'class' };
      } else if (ts.isInterfaceDeclaration(node)) {
        const name = node.name.getText(sourceFile);
        addDecl(relPath, name, 'interface', node, sourceFile);
        emitHeritage(node, relPath, name, sourceFile);
      } else if (ts.isTypeAliasDeclaration(node) || ts.isEnumDeclaration(node)) {
        addDecl(relPath, node.name.getText(sourceFile), 'type', node, sourceFile);
      } else if (ts.isMethodDeclaration(node) && node.name && enclosing) {
        const name = enclosing.fqn.split('#')[1] + '.' + node.name.getText(sourceFile);
        addDecl(relPath, name, 'method', node, sourceFile);
        enclosing = { fqn: symbolFQN(relPath, name), kind: 'method' };
      }

      // A call site. This is what the lexical analyzer cannot do: the checker
      // says which declaration the call actually reaches, so a local variable
      // shadowing an import does not produce a false edge.
      if (ts.isCallExpression(node) && enclosing) {
        emitCall(node, enclosing, sourceFile);
      }

      ts.forEachChild(node, (child) => walk(child, sourceFile, relPath, enclosing));
    }

    function addDecl(relPath, name, kind, node, sourceFile) {
      const { line } = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
      nodes.push({
        kind,
        name,
        fqn: symbolFQN(relPath, name),
        path: relPath,
        start_line: line + 1,
        visibility: isExported(node) ? 'exported' : 'unexported',
      });
      edges.push({
        src_kind: 'module', src_fqn: moduleFQN(relPath),
        dst_kind: kind, dst_fqn: symbolFQN(relPath, name),
        kind: 'contains', evidence: RESOLVED,
      });
    }

    function emitModuleEdge(node, sourceFile, relPath) {
      const spec = node.moduleSpecifier;
      if (!spec || !ts.isStringLiteral(spec)) return;
      // The compiler's own resolution, which honours tsconfig paths — the
      // thing the lexical analyzer has to guess at.
      const resolved = ts.resolveModuleName(spec.text, sourceFile.fileName, options, ts.sys);
      const target = resolved && resolved.resolvedModule && resolved.resolvedModule.resolvedFileName;
      if (target && isLocal(target)) {
        edges.push({
          src_kind: 'module', src_fqn: moduleFQN(relPath),
          dst_kind: 'module', dst_fqn: moduleFQN(rel(target)),
          kind: 'imports', evidence: RESOLVED,
        });
        return;
      }
      if (!spec.text.startsWith('.')) {
        const pkg = packageRoot(spec.text);
        nodes.push({ kind: 'dependency', name: pkg, fqn: 'npm:' + pkg });
        edges.push({
          src_kind: 'module', src_fqn: moduleFQN(relPath),
          dst_kind: 'dependency', dst_fqn: 'npm:' + pkg,
          kind: 'depends_on', evidence: DECLARED,
        });
      }
    }

    function emitHeritage(node, relPath, name, sourceFile) {
      for (const clause of node.heritageClauses || []) {
        const edgeKind = clause.token === ts.SyntaxKind.ImplementsKeyword ? 'implements' : 'extends';
        for (const t of clause.types) {
          const sym = checker.getSymbolAtLocation(t.expression);
          const target = declarationOf(sym);
          if (!target) continue;
          // The implementing type points at the interface: adding a method to
          // an interface must find the types that must change, and impact
          // analysis is a reverse traversal.
          edges.push({
            src_kind: node.kind === ts.SyntaxKind.ClassDeclaration ? 'class' : 'interface',
            src_fqn: symbolFQN(relPath, name),
            dst_kind: target.kind, dst_fqn: target.fqn,
            kind: edgeKind, evidence: RESOLVED,
          });
        }
      }
    }

    function emitCall(node, enclosing, sourceFile) {
      const sym = checker.getSymbolAtLocation(
        ts.isPropertyAccessExpression(node.expression) ? node.expression.name : node.expression);
      const target = declarationOf(sym);
      if (!target || target.fqn === enclosing.fqn) return;
      edges.push({
        src_kind: enclosing.kind, src_fqn: enclosing.fqn,
        dst_kind: target.kind, dst_fqn: target.fqn,
        kind: 'calls', evidence: RESOLVED,
      });
    }

    /**
     * declarationOf resolves a symbol to the declaration this repository owns,
     * following aliases so an imported name reaches its definition rather than
     * the import statement. Returns null for anything outside the repository:
     * a call into a dependency is real but out of scope for the call graph.
     */
    function declarationOf(sym) {
      if (!sym) return null;
      let s = sym;
      if (s.flags & ts.SymbolFlags.Alias) {
        try { s = checker.getAliasedSymbol(s); } catch { /* not an alias after all */ }
      }
      const decls = s.getDeclarations && s.getDeclarations();
      if (!decls || decls.length === 0) return null;
      const d = decls[0];
      const file = d.getSourceFile();
      if (!isLocal(file.fileName) || file.isDeclarationFile) return null;

      const name = declName(d, file);
      if (!name) return null;
      return { fqn: symbolFQN(rel(file.fileName), name), kind: declKind(d) };
    }
  }

  return { nodes, edges, diagnostics };
}

function declName(d, file) {
  if (ts.isMethodDeclaration(d) && d.parent && d.parent.name) {
    return d.parent.name.getText(file) + '.' + d.name.getText(file);
  }
  if (d.name && d.name.getText) return d.name.getText(file);
  return null;
}

function declKind(d) {
  if (ts.isClassDeclaration(d)) return 'class';
  if (ts.isInterfaceDeclaration(d)) return 'interface';
  if (ts.isMethodDeclaration(d)) return 'method';
  if (ts.isTypeAliasDeclaration(d) || ts.isEnumDeclaration(d)) return 'type';
  return 'function';
}

function isExported(node) {
  const mods = ts.getModifiers ? ts.getModifiers(node) : node.modifiers;
  return !!(mods || []).some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
}

function packageRoot(spec) {
  const parts = spec.split('/');
  if (spec.startsWith('@') && parts.length >= 2) return parts[0] + '/' + parts[1];
  return parts[0];
}

function readConfig(configPath, root, diagnostics) {
  const read = ts.readConfigFile(configPath, ts.sys.readFile);
  if (read.error) {
    diagnostics.push({
      level: 'error',
      message: 'tsconfig.json could not be read: ' +
        ts.flattenDiagnosticMessageText(read.error.messageText, ' '),
    });
    return null;
  }
  const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, path.dirname(configPath));
  if (parsed.errors && parsed.errors.length) {
    // Reported, not fatal: a config with a bad option still yields a file list
    // worth analysing, and silence here would look like an empty repository.
    for (const e of parsed.errors) {
      diagnostics.push({
        level: 'warning',
        message: ts.flattenDiagnosticMessageText(e.messageText, ' '),
      });
    }
  }
  return parsed;
}

/** collectSources finds TypeScript files when there is no tsconfig to ask. */
function collectSources(root) {
  const out = [];
  const skip = new Set(['node_modules', '.git', 'dist', 'build', 'out', 'coverage', '.next']);
  (function walk(dir) {
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
    for (const e of entries) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) {
        if (!skip.has(e.name)) walk(p);
        continue;
      }
      if (/\.(ts|tsx|mts|cts)$/.test(e.name) && !e.name.endsWith('.d.ts')) out.push(p);
    }
  })(root);
  return out;
}

module.exports = { analyze, moduleFQN, symbolFQN, packageRoot };

if (require.main === module) {
  process.exit(main(process.argv));
}

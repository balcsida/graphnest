// Test-only: capture the pinned producer's file-selection and hashing rules from its built dist/.
import { createRequire } from 'node:module';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import assert from 'node:assert/strict';

const SOURCE_PROBES = ['a.ts', 'A.TS', 'a.d.ts', 'a.tsx', 'a.js', 'a.mjs', 'a.go', 'a.py', 'a.rs', 'a.java', 'a.kt', 'a.rb', 'a.swift', 'a.vue', 'a.svelte', 'a.h', 'a.inc', 'a.json', 'package.json', 'codegraph.json', 'templates/index.json', 'templates/customers/login.json', 'sections/header.json', 'config/settings.json', 'conf/routes', 'app/conf/routes', 'a.routes', 'myapp.app', 'myapp.app.src', 'a.app.srcx', 'README.md', 'a.yaml', 'a.yml', 'a.xml', 'a.sql', 'a.liquid', 'Makefile', 'Dockerfile', 'a.txt', 'a', '.gitignore', 'a.min.js', 'a.pb.go', 'a.dota_lua'];
const IGNORE_PROBES = ['Dist/a.ts', 'NODE_MODULES/a.js', 'distribution/a.ts', 'foo.egg-info/a.py', 'cmake-build-debug/a.c', 'bazel-out/a.go', 'bazel/a.go', 'res/layout/a.xml', 'x/res/values-es/a.xml', 'res/raw/a.sql', 'res/layouts/a.xml', 'src/main/java/com/x/build/A.java', 'src/test/kotlin/x/build/A.kt', 'app/build/A.java', 'src/build/A.java', 'a/b/c.ts', '.cache/a.ts', 'a/.cache/b.ts', 'vendor/a.go', 'src/vendor/a.go', 'target/A.class', 'out/a.ts', 'output/a.ts', 'a/dist/b/c.ts'];
const HASH_VECTORS = ['68656c6c6f', '610d0a62', 'efbbbf41', 'e282acf09f9880', 'f09f984341', 'ffff', 'e08080', 'c0af', 'eda080', '41e282', 'f4908080', '80', 'c241', 'f09f98', 'e282ac41e2', ''];
const OVERSIZE_PROBE = 1048577;

export function captureProducerRules(upstream) {
  const require = createRequire(`${upstream}/package.json`);
  const { EXTENSION_MAP, isSourceFile } = require(`${upstream}/dist/extraction/grammars.js`);
  const extraction = require(`${upstream}/dist/extraction/index.js`);
  const { buildDefaultIgnore, hashContent } = extraction;

  const empty = mkdtempSync(join(tmpdir(), 'graphnest-producer-rules-'));
  let ig;
  try {
    ig = buildDefaultIgnore(empty);
  } finally {
    rmSync(empty, { recursive: true, force: true });
  }
  const manager = ig._rules;
  assert.ok(manager && Array.isArray(manager._rules) && typeof manager._ignoreCase === 'boolean', 'ignore package internals changed: expected ig._rules._rules and ig._rules._ignoreCase');
  const patterns = manager._rules.map(rule => rule.pattern);
  assert.ok(patterns.length > 0 && patterns.every(pattern => typeof pattern === 'string'), 'ignore rules have no pattern strings');

  const directories = patterns.filter(pattern => pattern.endsWith('/') && !pattern.startsWith('!')).map(pattern => pattern.slice(0, -1));
  const ignoreProbes = [...directories.flatMap(dir => [`${dir}/x.ts`, `a/${dir}/x.ts`]), ...IGNORE_PROBES];

  const limits = existsSync(`${upstream}/dist/file-limits.js`) ? require(`${upstream}/dist/file-limits.js`) : null;
  const rules = {
    extension_map: { ...EXTENSION_MAP },
    source_file_decisions: Object.fromEntries(SOURCE_PROBES.map(probe => [probe, isSourceFile(probe)])),
    default_ignore_patterns: patterns,
    ignore_case: manager._ignoreCase,
    ignore_decisions: Object.fromEntries(ignoreProbes.map(probe => [probe, ig.ignores(probe)])),
    content_hash_vectors: HASH_VECTORS.map(hex => {
      const text = Buffer.from(hex, 'hex').toString('utf8');
      return { bytes_hex: hex, sha256: hashContent(text), code_points: [...text].map(c => c.codePointAt(0).toString(16)) };
    }),
  };
  if (limits) {
    rules.max_source_file_size_bytes = limits.MAX_SOURCE_FILE_SIZE_BYTES;
    rules.oversize_hash = { size: OVERSIZE_PROBE, input: limits.oversizeStamp(OVERSIZE_PROBE), sha256: hashContent(limits.indexedHashInput(OVERSIZE_PROBE, () => 'x')) };
  } else {
    // 1.6.0 hashes the full content of every file it indexes; its MAX_FILE_SIZE only gates indexing.
    rules.max_source_file_size_bytes = typeof extraction.MAX_FILE_SIZE === 'number' ? extraction.MAX_FILE_SIZE : 1048576;
    if (typeof extraction.MAX_FILE_SIZE !== 'number') rules.max_source_file_size_source = 'src/extraction/index.ts MAX_FILE_SIZE';
    rules.oversize_hash = null;
  }
  return rules;
}

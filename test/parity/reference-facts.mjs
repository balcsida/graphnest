// Test-only adapter: index the shared sources with the pinned producer and keep only the database facts.
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
import { writeFileSync } from 'node:fs';
import os from 'node:os';
import assert from 'node:assert/strict';
import { captureProducerRules } from './producer-rules.mjs';
const [upstream, root, metrics] = process.argv.slice(2);
const require = createRequire(`${upstream}/package.json`);
const { CodeGraph } = require(`${upstream}/dist/index.js`);
const start = performance.now();
const graph = await CodeGraph.init(root);
try {
  const result = await graph.indexAll();
  if (!result.success || result.errors?.length) throw new Error(JSON.stringify(result));
  const index_ms = performance.now() - start;
  assert.equal(graph.getNodesByName('mustNotBeIndexed').length, 0);
  writeFileSync(`${metrics}.rules`, JSON.stringify(captureProducerRules(upstream)));
  writeFileSync(metrics, JSON.stringify({ index_ms, result, machine: { platform: process.platform, architecture: process.arch, os_release: os.release(), os_version: os.version(), cpu_model: os.cpus()[0]?.model ?? 'unknown', logical_cpus: os.cpus().length, node: process.version, sqlite: process.versions.sqlite, v8: process.versions.v8 } }));
} finally {
  graph.close();
}

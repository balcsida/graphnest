// Test-only adapter: execute the pinned producer without invoking install/watch/MCP hooks.
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
import { writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';
const [upstream, root, metrics] = process.argv.slice(2);
const require = createRequire(`${upstream}/package.json`);
const { CodeGraph } = require(`${upstream}/dist/index.js`);
const start = performance.now();
const graph = await CodeGraph.init(root);
try {
  const result = await graph.indexAll();
  if (!result.success || result.errors?.length) throw new Error(JSON.stringify(result));
  const index_ms = performance.now() - start;
  const normalize = graph.getNodesByName('normalize').find(node => node.kind === 'function');
  const service = graph.getNodesByName('Service').find(node => node.kind === 'class');
  const run = graph.getNodesByName('run').find(node => node.kind === 'function');
  assert.ok(normalize && service && run);
  const answers = {
    'lib-searchNodes': graph.searchNodes('normalize'),
    'lib-getCallers': graph.getCallers(normalize.id),
    'lib-getCallees': graph.getCallees(run.id),
    'lib-getCallGraph': graph.getCallGraph(normalize.id),
    'lib-getTypeHierarchy': graph.getTypeHierarchy(service.id),
    'lib-findUsages': graph.findUsages(normalize.id),
    'lib-getImpactRadius': graph.getImpactRadius(normalize.id),
    'lib-findPath': graph.findPath(run.id, normalize.id, ['calls']),
    'lib-getFileDependencies': graph.getFileDependencies('main.ts'),
    'lib-getFileDependents': graph.getFileDependents('core.ts'),
    'lib-getContext': graph.getContext(normalize.id),
    'lib-getCode': await graph.getCode(normalize.id),
    'lib-findRelevantContext': await graph.findRelevantContext('normalize'),
  };
  assert.ok(answers['lib-getCallers'].some(({node}) => node.name === 'greet'));
  assert.ok(answers['lib-getCallees'].some(({node}) => node.name === 'normalize'));
  assert.ok(answers['lib-getFileDependencies'].includes('core.ts'));
  assert.ok(answers['lib-getCode'].includes('return name.trim()'));
  assert.equal(graph.getNodesByName('mustNotBeIndexed').length, 0);
  const timings = [];
  for (let i = 0; i < 105; i++) {
    const started = performance.now();
    graph.getCallers(normalize.id);
    timings.push(performance.now() - started);
  }
  timings.splice(0, 5);
  timings.sort((a, b) => a - b);
  writeFileSync(`${metrics}.answers`, JSON.stringify(answers, (key, value) => {
    if (key === 'updatedAt' || key === 'indexedAt' || key === 'modifiedAt') return 0;
    if (value instanceof Map) return [...value.values()];
    if (value instanceof Set) return [...value];
    return typeof value === 'string' ? value.replaceAll(root, '/fixture') : value;
  }));
  writeFileSync(metrics, JSON.stringify({ index_ms, result, library_getCallers_ms: { samples: 100, p50: timings[49], p95: timings[94] } }));
} finally {
  graph.close();
}

// Test-only adapter: execute the pinned producer without invoking install/watch/MCP hooks.
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
import { readFileSync, writeFileSync, utimesSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import assert from 'node:assert/strict';
const [upstream, root, metrics] = process.argv.slice(2);
const require = createRequire(`${upstream}/package.json`);
const { CodeGraph } = require(`${upstream}/dist/index.js`);
const { ToolHandler } = require(`${upstream}/dist/mcp/tools.js`);
const { buildFlow } = require(`${upstream}/dist/ui-server/api/flow.js`);
const { buildSteps } = require(`${upstream}/dist/ui-server/api/steps.js`);
const { buildScreens } = require(`${upstream}/dist/ui-server/api/screens.js`);
const { buildMap } = require(`${upstream}/dist/ui-server/api/map.js`);
const { buildNode } = require(`${upstream}/dist/ui-server/api/node.js`);
const { buildFile } = require(`${upstream}/dist/ui-server/api/file.js`);
const { buildSource } = require(`${upstream}/dist/ui-server/api/source.js`);
const { buildEntryPoints } = require(`${upstream}/dist/ui-server/api/entrypoints.js`);
const { buildDeadCode } = require(`${upstream}/dist/ui-server/api/deadcode.js`);
const { buildTrails, saveTrail, removeTrail, resolveHop } = require(`${upstream}/dist/ui-server/api/trails.js`);
const params = values => new URLSearchParams(values);
async function refusal(action, code) {
  try { await action(); } catch (error) {
    assert.equal(error.code, code);
    return { code: error.code, message: error.message };
  }
  assert.fail(`expected ${code}`);
}
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
  const tool = new ToolHandler(graph);
  answers['mcp-explore-source'] = await tool.execute('codegraph_explore', { query: 'processGreeting', projectPath: root });
  answers['mcp-explore-unmatched-fallback'] = await tool.execute('codegraph_explore', { query: 'MissingFixtureSymbol987', projectPath: root });
  assert.deepEqual(graph.getNodesByName('MissingFixtureSymbol987'), []);
  assert.ok(!answers['mcp-explore-source'].isError);
  assert.ok(answers['mcp-explore-source'].content.some(item => item.text?.includes("return 'skipped'")));
  answers['ui-flow-branch'] = await buildFlow(graph, root, params({ from: 'processGreeting', to: 'run' }));
  answers['ui-flow-missing'] = await buildFlow(graph, root, params({ from: 'processGreeting', to: 'MissingFixtureSymbol987' }));
  answers['ui-flow-invalid'] = await refusal(() => buildFlow(graph, root, params({ from: 'run', to: 'run' })), 'bad-request');
  assert.ok(answers['ui-flow-branch'].flows.length > 0);
  assert.ok(answers['ui-flow-branch'].flows.some(flow => flow.hops.some(hop => hop.edge?.when === 'enabled' && hop.edge.line === 4)));
  assert.equal(answers['ui-flow-missing'].flows.length, 0);
  assert.ok(answers['ui-flow-missing'].unresolved.includes('MissingFixtureSymbol987'));
  answers['ui-steps-branch'] = await buildSteps(graph, root, params({ symbol: 'processGreeting' }));
  assert.ok(answers['ui-steps-branch'].program.root.some(step => step.kind === 'fork' && step.on === 'enabled'));
  answers['ui-steps-screen'] = await buildSteps(graph, root, params({ symbol: 'Home' }));
  assert.ok(answers['ui-steps-screen'].links.some(link => link.when.includes('enabled')));
  answers['ui-screens-navigation'] = await buildScreens(graph, root);
  assert.equal(answers['ui-screens-navigation'].routed, true);
  assert.ok(answers['ui-screens-navigation'].links.length > 0);
  assert.ok(answers['ui-screens-navigation'].links.some(link => link.when === 'enabled' && link.sites.some(site => site.href === '/details' && site.line === 4)));
  answers['ui-map-modules'] = buildMap(graph, root, params({ root: '.', depth: '1' }));
  assert.ok(answers['ui-map-modules'].links.some(link => link.source === 'main.ts' && link.target === '(root files)' && link.byKind.some(row => row.kind === 'imports')));
  answers['ui-node-types'] = await buildNode(graph, root, service.id);
  assert.deepEqual(answers['ui-node-types'].hierarchy.ancestors.items.map(node => node.name).sort(), ['Base', 'Greeter']);
  assert.equal(answers['ui-node-types'].members.items.find(node => node.name === 'greet').overrides.baseTypeName, 'Base');
  answers['ui-node-missing'] = await refusal(() => buildNode(graph, root, 'missing-fixture-id'), 'not-found');
  answers['ui-file-public-members'] = buildFile(graph, root, 'core.ts');
  assert.equal(answers['ui-file-public-members'].outline.items.find(node => node.name === 'Service').exported, true);
  assert.ok(!answers['ui-file-public-members'].outline.items.find(node => node.name === 'dormantUtility').exported);
  answers['ui-source-verbatim'] = await buildSource(graph, root, params({ file: 'consumer.ts', from: '2', to: '7' }));
  assert.deepEqual(answers['ui-source-verbatim'].lines, readFileSync(`${root}/consumer.ts`, 'utf8').split('\n').slice(1, 7));
  answers['ui-source-invalid-range'] = await refusal(() => buildSource(graph, root, params({ file: 'consumer.ts', from: '7', to: '2' })), 'bad-request');
  answers['ui-entrypoints'] = buildEntryPoints(graph, params({}));
  assert.ok(answers['ui-entrypoints'].tests.items.some(node => node.file === 'consumer.test.ts'));
  answers['ui-deadcode'] = buildDeadCode(graph, root, params({}));
  assert.ok(answers['ui-deadcode'].rows.items.some(node => node.name === 'dormantUtility'));
  assert.ok(!answers['ui-deadcode'].rows.items.some(node => node.name === 'orphanUtility'));
  assert.ok(answers['ui-deadcode'].excluded.some(row => row.reason === 'unreachableFile' && row.count > 0));
  const affected = files => JSON.parse(execFileSync(process.execPath, ['--no-warnings', `${upstream}/dist/bin/codegraph.js`, 'affected', ...files, '--path', root, '--json'], { encoding: 'utf8', env: process.env }));
  answers['cli-affected-transitive'] = affected(['core.ts']);
  answers['cli-affected-unrelated'] = affected(['orphan.ts']);
  assert.deepEqual(answers['cli-affected-transitive'].affectedTests, ['consumer.test.ts']);
  assert.deepEqual(answers['cli-affected-unrelated'].affectedTests, []);
  const original = readFileSync(`${root}/consumer.ts`);
  try {
    writeFileSync(`${root}/consumer.ts`, '// modified after indexing\n');
    answers['ui-source-drift'] = await buildSource(graph, root, params({ file: 'consumer.ts' }));
    assert.equal(answers['ui-source-drift'].drift, true);
    assert.ok(!answers['ui-source-drift'].lines);
  } finally {
    writeFileSync(`${root}/consumer.ts`, original);
    utimesSync(`${root}/consumer.ts`, 0, 0);
  }
  const writable = { readOnly: false, readOnlyReason: null };
  const readOnly = { readOnly: true, readOnlyReason: 'Fixture reader' };
  answers['ui-trails-empty'] = buildTrails(graph, root, writable);
  assert.deepEqual(answers['ui-trails-empty'].trails, []);
  const trail = { name: 'Greeting to normalization', note: 'Source-evidenced call', hops: [{ id: run.id }, { id: normalize.id, dir: 'down' }] };
  answers['ui-trails-save'] = saveTrail(graph, root, trail, writable);
  answers['ui-trails-replace'] = saveTrail(graph, root, { ...trail, note: 'Updated fixture note' }, writable);
  answers['ui-trails-reload'] = buildTrails(graph, root, writable);
  answers['ui-trails-readonly'] = await refusal(() => saveTrail(graph, root, trail, readOnly), 'refused');
  answers['ui-trails-missing-hop'] = await refusal(() => saveTrail(graph, root, { ...trail, hops: [{ id: 'missing-fixture-id' }] }, writable), 'bad-request');
  answers['ui-trails-resolve-missing'] = resolveHop(graph, { dir: 'start', id: 'missing-fixture-id', name: 'gone', qualifiedName: 'gone', kind: 'function', file: 'removed.ts', line: 1 });
  assert.equal(answers['ui-trails-save'].replaced, false);
  assert.equal(answers['ui-trails-replace'].replaced, true);
  assert.equal(answers['ui-trails-reload'].trails[0].intact, true);
  assert.ok(answers['ui-trails-reload'].trails[0].encoded);
  const savedHops = answers['ui-trails-reload'].trails[0].encoded.split(',').map(hop => hop[0] + decodeURIComponent(hop.slice(1)));
  const savedQuery = new URLSearchParams();
  for (const hop of savedHops) savedQuery.append('hop', hop);
  answers['ui-trails-open-flow'] = await buildFlow(graph, root, savedQuery);
  assert.deepEqual(answers['ui-trails-open-flow'].flows[0].hops.map(hop => hop.node.name), ['run', 'normalize']);
  assert.equal(answers['ui-trails-resolve-missing'].status, 'missing');
  answers['ui-trails-delete'] = removeTrail(graph, root, answers['ui-trails-save'].saved, writable);
  assert.deepEqual(answers['ui-trails-delete'].trails, []);
  const timings = [];
  for (let i = 0; i < 105; i++) {
    const started = performance.now();
    graph.getCallers(normalize.id);
    timings.push(performance.now() - started);
  }
  timings.splice(0, 5);
  timings.sort((a, b) => a - b);
  writeFileSync(`${metrics}.answers`, JSON.stringify(answers, (key, value) => {
    if (['updatedAt', 'createdAt', 'indexedAt', 'modifiedAt', 'lastIndexedAt', 'elapsedMs'].includes(key)) return typeof value === 'string' ? '1970-01-01T00:00:00.000Z' : 0;
    if (key === 'author') return 'GraphNest fixture';
    if (value instanceof Map) return [...value.values()];
    if (value instanceof Set) return [...value];
    return typeof value === 'string' ? value.replaceAll(root, '/fixture') : value;
  }));
  writeFileSync(metrics, JSON.stringify({ index_ms, result, library_getCallers_ms: { samples: 100, p50: timings[49], p95: timings[94] } }));
} finally {
  graph.close();
}

// Test-only: ask the pinned producer the Stage 2 gate questions of an already indexed repository.
// node gate-answers.mjs <upstream-dir> <repo-root> <out.json> <symbol> <file>
import { createRequire } from 'node:module';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
const [upstream, root, out, symbol, file] = process.argv.slice(2);
assert.ok(upstream && root && out && symbol && file, 'usage: gate-answers.mjs <upstream-dir> <repo-root> <out.json> <symbol> <file>');
const require = createRequire(`${upstream}/package.json`);
const { CodeGraph } = require(`${upstream}/dist/index.js`);
const { ToolHandler } = require(`${upstream}/dist/mcp/tools.js`);
const { version } = JSON.parse(readFileSync(`${upstream}/package.json`, 'utf8'));
// Open the existing index read-only: no init, no indexAll, no sync.
const graph = await CodeGraph.open(root, { readOnly: true });
try {
  const tool = new ToolHandler(graph);
  const directory = path.posix.dirname(file);
  const questions = {
    callers: ['codegraph_callers', { symbol }],
    callees: ['codegraph_callees', { symbol }],
    impact: ['codegraph_impact', { symbol, depth: 2 }],
    explore: ['codegraph_explore', { query: symbol }],
    files: ['codegraph_files', { ...(directory === '.' ? {} : { path: directory }), format: 'flat' }],
  };
  const answers = {};
  for (const [id, [name, args]] of Object.entries(questions)) {
    answers[id] = await tool.execute(name, { ...args, projectPath: root });
    assert.ok(!answers[id].isError, `${id}: ${JSON.stringify(answers[id])}`);
  }
  writeFileSync(out, JSON.stringify({ producer: { name: 'codegraph', version }, symbol, file, answers }, null, 2) + '\n');
} finally {
  graph.close();
}

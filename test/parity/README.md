# Pinned CodeGraph reference

This is test-only oracle tooling. It does not add a producer, importer, Node
runtime, or native dependency to GraphNest binaries. The sources in
`test/fixtures/codegraph/source/` are task-owned and never installed as packages.
Their fake `expo-router` dependency is detection metadata, not an install target.

Run the committed reference checks offline with Python 3 (stdlib only):

```sh
make parity-reference
```

To regenerate, use **Node 24.13.0** with npm, Python 3.12+, and a separate clean
producer clone:

```sh
git clone https://github.com/colbymchenry/codegraph.git /tmp/codegraph-reference
git -C /tmp/codegraph-reference checkout --detach b9ca4b7981116909900368cc1686a1074cd4d4c1
cd /path/to/graphnest
python3 test/parity/generate_reference.py --upstream /tmp/codegraph-reference
python3 test/parity/generate_reference.py --upstream /tmp/codegraph-reference --check
make parity-reference
```

`--node /path/to/node` selects the exact pinned runtime. The generator checks the
Git commit, tracked-source cleanliness, runtime, source hashes, complete source
file set, schema, full logical database facts, and real library query answers.
It extracts two independent temporary source copies on each invocation. `--check`
does not update committed artifacts. Every invocation archives the pinned Git
commit into a fresh temporary build tree, runs `npm ci --ignore-scripts
--no-audit --no-fund`, TypeScript compilation, and `npm run copy-assets` there.
Existing checkout `dist/` and `node_modules/` cannot influence the producer.
Regeneration and `--check` may access the network and populate npm's dependency
cache; `make parity-reference` stays offline. Compilation and asset copying use
upstream scripts; installation lifecycle scripts, UI build, native kernel, CLI setup,
watch, MCP registration, telemetry and user indexes are unnecessary. Extraction
receives a fresh empty temporary HOME and only `PATH` (selected Node directory
plus system binary directories), temporary `TMPDIR`, `CODEGRAPH_KERNEL=0`,
`CODEGRAPH_NO_RELAUNCH=1`, `DO_NOT_TRACK=1`, `LANG=C.UTF-8`, `LC_ALL=C.UTF-8`, and
`TZ=UTC`. Caller feature flags, Node options and global CodeGraph configuration
are excluded. The build environment separately allows HOME for npm's cache and
proxy/certificate settings for dependency access.

The live isolation regression creates its own clone with a throwing stale
`dist/index.js` and hostile caller `CODEGRAPH_*` flags, then runs `--check`:

```sh
sh test/parity/check_stale_dist.sh /tmp/codegraph-reference /path/to/node
```

`reference.db` is a genuine producer database, backed up after close, with time
columns zeroed and temporary source-root text replaced by `/fixture`. Schema and
facts are otherwise retained, including unresolved references and metadata. FTS
is exercised by real `searchNodes`/`findRelevantContext` answers. Hashes cover the
database, source configuration, sources, schema and expected answers. SQLite
physical layout is not the determinism contract; complete logical rows are.

`expected.json` records ordered SQL facts. `library-expected.json` records 13
actual CodeGraph library methods, including search, callers/callees, call graph,
hierarchy, usage, impact, path, dependencies, context and source reads. The adapter
independently asserts source-evidenced calls, imports, source text and exclusion.
`unicode.ts` uses CRLF, accented text and an astral character before a call for
future coordinate-conversion checks. Synthetic records in
`synthetic-contract.json` cover the exact 23-kind/13-relation vocabulary; **they
were not extracted** and do not prove reachable producer behavior. The manifest
lists the actual extracted vocabulary, and the inventory records coverage gaps.
The current real sources emit 20 node kinds and nine relation kinds. `protocol`,
`parameter`, `export`, `exports`, `type_of`, `returns` and `overrides` remain
synthetic contract coverage, without an extracted-behavior claim.

`baseline.json` records one portable producer index and 100 warm direct-library
caller queries (five warmups discarded), plus a separate direct SQLite query.
It is informational and machine-specific, excluded from reproducibility hashes.
It does not measure GraphNest, browser latency, native-kernel performance,
incremental indexing or installation/watch workflows. Those remain later gates.

The upstream schema is distributed under `../fixtures/codegraph/UPSTREAM-LICENSE`.

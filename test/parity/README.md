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

`expected.json` records ordered SQL facts. `library-expected.json` records 41
actual producer query/workflow answers. The manifest lists their exact task IDs.
The first 13 cover library search, callers/callees, call graph, hierarchy, usage,
impact, path, dependencies, context and source reads. The additional answers run
the upstream MCP tool handler, viewer API builders, affected-test CLI and saved
trail services directly; they do not simulate those implementations.

| Workflow | Oracle task IDs and independently checked evidence |
| --- | --- |
| Explore | `mcp-explore-source` includes verbatim `processGreeting` source. `mcp-explore-unmatched-fallback` records the pinned engine's unrelated fallback sources for an absent symbol; it is not a correct-match or no-results claim. |
| Conditional flow and steps | `ui-flow-branch` records the `enabled` guard at consumer.ts:4; `ui-flow-missing` and `ui-flow-invalid` preserve absence/refusal. `ui-steps-branch` records the program fork; `ui-steps-screen` records conditional screen traversal. |
| Navigation and maps | `ui-screens-navigation` records `/` to `/details` with the `enabled` guard. `ui-map-modules` records concrete cross-module imports. |
| Types and public members | `ui-node-types` finds Base/Greeter ancestors and the inferred greet override. `ui-file-public-members` distinguishes exported Service from private dormantUtility. `ui-node-missing` preserves the missing-id refusal. These views do not create dormant type_of/returns/overrides edges. |
| Source | `ui-source-verbatim` matches the exact source lines. `ui-source-invalid-range` refuses reversed bounds; `ui-source-drift` omits lines after a temporary source change. The original bytes and mtime are restored. |
| Entry points and dead code | `ui-entrypoints` identifies consumer.test.ts. `ui-deadcode` reports dormantUtility in a reachable file and excludes the wholly unreachable orphan.ts with its explicit reason. |
| Affected tests | `cli-affected-transitive` finds consumer.test.ts across core.ts → main.ts → consumer.ts imports; `cli-affected-unrelated` finds none for orphan.ts. Source fixtures are analyzed, never executed. |
| Saved trails | `ui-trails-*` covers empty/list/create/replace/reload/delete, read-only and missing-hop refusals, missing-symbol resolution, and reopening the encoded saved trail as an actual run → normalize flow. All writes stay inside the temporary fixture's `.codegraph/ui/trails/`. |

The adapter and offline test independently assert source-evidenced positive and
negative results. The larger golden answers retain every returned field, except
wall-clock timings/index timestamps and saved-trail dates/author are canonicalized
for reproducibility. These are reference-service checks, not browser interaction
or GraphNest implementation checks.

Remaining S1.01 oracle work includes native browser interactions and SVG/PNG
exports (client-side code, not a trail-service endpoint), HTTP/MCP transport
contracts, more query limits/filter/error variants, routed-API and language/
framework matrices, and richer steps such as stores/native bridges/events.
Only exported flags/member outlines and actual type hierarchy are covered here;
this does not claim a separate public-surface/type-users/returners query where
the pinned producer exposes none. Actual GraphNest serialization, PostgreSQL,
REST, MCP, UI and authorization comparisons remain the later implementation
layers and S1.10 gate. No planned case is counted as a passing test.

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

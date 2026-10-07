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

A second, facts-only pin (CodeGraph 1.6.2, commit `6560052a6f856855d3f71eee838fd66ccfa4285d`, schema 11)
indexes the same `source/` files into `test/fixtures/codegraph-1.6.2/`. Use a clone checked out at that
commit and select it with `--pin 1.6.2` (default `1.6.0`); `--timings` is not available for it:

```sh
git -C /tmp/codegraph-reference checkout --detach 6560052a6f856855d3f71eee838fd66ccfa4285d
python3 test/parity/generate_reference.py --upstream /tmp/codegraph-reference --pin 1.6.2
python3 test/parity/generate_reference.py --upstream /tmp/codegraph-reference --pin 1.6.2 --check
```

It runs `reference-facts.mjs` (init, index, assert success and the excluded file absent) and
adds `CODEGRAPH_NO_DAEMON`, `CODEGRAPH_NO_UPDATE_CHECK` and `CODEGRAPH_NO_WATCH` to its recorded
environment. `make parity-reference` also validates it offline.

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

`expected.json` records ordered SQL facts. `library-expected.json` records 52
actual producer query/workflow answers. The manifest lists their exact task IDs.
The first 13 cover library search, callers/callees, call graph, hierarchy, usage,
impact, path, dependencies, context and source reads. The additional answers run
the upstream MCP tool handler, viewer API builders, affected-test CLI and saved
trail services directly; they do not simulate those implementations.

| Workflow | Oracle task IDs and independently checked evidence |
| --- | --- |
| Symbol tools | `mcp-callers-*`, `mcp-callees-*` and `mcp-impact-*` run the upstream `codegraph_callers`, `codegraph_callees` and `codegraph_impact` handlers. They cover grouping of same-named definitions (`normalize`, `greet`, `identity`), `file` narrowing and its no-match fallback, qualified names (`Service.greet`), limits, depth, an absent symbol, and `via instantiation`/`via import` labels. |
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

Remaining reference coverage for later conformance layers includes native browser interactions and SVG/PNG
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

`workflow-baseline.json` separately records five consecutive in-process runs of
100 useful warmed responses for `getCallers`, MCP `explore` and the conditional
source flow. Refresh only this machine-specific artifact after full oracle
verification with:

```sh
python3 test/parity/generate_reference.py --upstream /tmp/codegraph-reference \
  --node /path/to/node --check --timings
make parity-reference
```

Each run discards five warmups, then measures the complete query plus JSON
serialization with `performance.now()`. Every warmup and measured response must
contain its required caller, verbatim source or guarded flow. Assertions occur
outside the timed interval. Explore uses one retained handler without any
`ExploreSessionState`, query pool or watcher, so session dedup cannot substitute
an empty response. All queries share the same open index and warm upstream
caches. Exact arguments, result limits and the numeric adaptive explore budget
are recorded. Raw samples, nearest-rank p50/p95 and medians across five runs are
retained, with response-byte ranges and RSS for each run.

RSS is the main Node process: current RSS after the run and the cumulative
high-water mark through that point, including indexing, earlier oracle work and
assertions; it excludes child processes and is not per-query allocation. The
artifact fingerprints producer/configuration, source bytes, both executed harness
scripts, reference database and answers, and records CPU/OS/Node/SQLite identity.
The offline check verifies fingerprints, sample counts and summary arithmetic.

A completed query over 5 seconds fails capture; a hung adapter is terminated by
the 120-second subprocess deadline. Those are measurement safeguards, not new
release latency gates. Errors, missing required answers and timeouts cannot be
omitted from a successful capture. `--timings` requires `--check`; graph facts and
deterministic answers are never rewritten by timing refresh. Machine-specific
times remain outside oracle equality. Existing GraphNest 10% and future local
1.25x budgets are unchanged; this is not a local parity pass. Browser/client
exports, imports/publication, transports, cold startup and large-corpus results
remain explicitly unmeasured.

## Producer rules

Both pins also write `producer-rules.json` into their fixture directory (listed in `manifest.json`).
`producer-rules.mjs` captures it from the freshly built `dist/` after indexing, and `--check`
compares a regeneration with the committed file. It records the producer's own answers, never a
transcription of its source:

- `extension_map` (`EXTENSION_MAP`) and `source_file_decisions` (`isSourceFile` over a fixed probe list).
- `default_ignore_patterns` of `buildDefaultIgnore` on an empty directory (no `.gitignore` merged),
  `ignore_case`, and `ignore_decisions` for every directory pattern plus fixed probes. The patterns come
  from the npm `ignore` internals (`ig._rules._rules`); capture fails if that shape changes.
- `max_source_file_size_bytes` and `oversize_hash` (the size-stamp hash, `null` for 1.6.0, which hashes
  full content; its `MAX_FILE_SIZE` is not exported, so 1048576 is recorded with a source note).
- `content_hash_vectors`: `hashContent` of `Buffer.toString('utf8')` for valid and invalid byte strings,
  with the decoded code points.

`internal/graphimport/codegraph-rules-*.json` are embedded copies; a Go test requires them to be
byte-identical to these fixtures, so regenerate and copy together.

The upstream schema is distributed under `../fixtures/codegraph/UPSTREAM-LICENSE`.

## Gate answers for a real repository

`gate-answers.mjs` asks an already indexed repository the Stage 2 gate questions through the pinned
CodeGraph `ToolHandler` and writes the raw tool results. It opens the existing index read-only and never indexes:

```sh
env -i PATH=<pinned node dir>:/usr/bin:/bin HOME="$(mktemp -d)" CODEGRAPH_KERNEL=0 CODEGRAPH_NO_RELAUNCH=1 \
  DO_NOT_TRACK=1 CODEGRAPH_NO_DAEMON=1 CODEGRAPH_NO_WATCH=1 \
  node test/parity/gate-answers.mjs <upstream-dir> <repo-root> <out.json> <symbol> <file>
```

`<file>` is a repository-relative path; `codegraph_files` is asked for its directory. Point
`GRAPHNEST_GATE_CODEGRAPH_ANSWERS` at `<out.json>` when running `TestCodeGraphImportGate` (`test/e2e`).

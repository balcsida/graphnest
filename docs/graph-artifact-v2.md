# Graph artifact v2 contract

This is the S1.02 interchange contract for CodeGraph facts and evidence. Storage
and publication remain v1 until S1.03 supplies the richer schema. It adds no
production importer, SQLite dependency, Node runtime, or analyzer framework.

`internal/graphartifact/v2/artifact_v2.proto` is the model and wire definition;
the generated Go messages are used directly to avoid a second lossy model.
`graphartifact.ParseV2`, `ValidateV2`, and `MarshalV2` operate on this model.
The existing `Parse`, `Validate`, `Identity`, scanner, enrichment runner, upload,
transactional graph replacement, and queue completion remain strictly v1.
Passing v2 bytes through the v1 reader is rejected. Valid v1 wire values,
generated code, and the existing identity golden are unchanged; out-of-range
wire enums are rejected before conversion to the v1 `uint8` model.

## Facts and presence

Nodes preserve original producer IDs, separate declaration occurrences, all 23
CodeGraph kind names, short and qualified names, language, optional source path,
documentation, signature, visibility, modifiers, decorators, type parameters,
return type, SCIP symbol, and update timestamp. `repository` and `symbol` also
remain available for GraphNest/SCIP representations. Kind names are validated;
unknown kinds require a contract decision rather than silent relabeling.

Edges preserve source row IDs, independent occurrences, endpoint occurrences,
relationship kind, optional location and double-precision confidence,
provenance, resolution reason, and namespaced extension data. Confidence absent
and confidence zero are different. Multiple calls between identical endpoints
remain separate, even when the producer provides no location. References can
also retain CodeGraph's internal `function_ref` kind without inventing a new
published relationship.

Files require full lowercase SHA-256 hashes and preserve language, size, modification/index timestamps, node count,
generation hints, and the original optional extraction-error array. Unresolved
references preserve original row ID, occurrence, source, name/kind, candidates,
path/language, status, and name tail. Structured diagnostics preserve message,
severity, code, location, and extensions. Project metadata retains key, value,
and update timestamp. Extensions preserve bounded JSON, including unknown
producer evidence fields. Test-only conversion retains complete original edge
metadata alongside its typed confidence/resolution fields.

Optional scalar fields and wrapper messages distinguish absence from present
empty strings/lists or false/zero. A virtual/external node can have neither path
nor location. An explicitly empty original path remains distinguishable from
an absent path; it does not create a source location. A location may have just a
path, a partial start point, or start/end points. Line and column are independently
optional, so line-only and column-only evidence remain partial. An end point
requires a start point. Comparable positions must be ordered.

## Relationship registry

`graphartifact.Relationships`, `ParseRelationship`, and `RelationshipFromWire`
centralize numeric mappings, producer/API names, and outgoing/incoming labels.
Every relationship is directed from source to target. Unknown numeric values
are rejected before narrowing. Future API layers should use this registry;
this stage does not expand the current v1 API's accepted values.

| Value | Name | Value | Name |
| --- | --- | --- | --- |
| 1 | contains | 8 | type_of |
| 2 | imports | 9 | returns |
| 3 | references | 10 | instantiates |
| 4 | calls | 11 | overrides |
| 5 | extends | 12 | decorates |
| 6 | implements | 13 | navigates |
| 7 | exports | | |

Values 1–6 retain GraphNest's v1 numbering. CodeGraph's enum order is different.

## Identity and semantic hashes

`IdentityV2(producer, repository, sourceID, occurrence)` hashes length-prefixed
fields under a v2 domain. The scope includes stable public repository identity,
producer name/version/configuration, original ID, and declaration occurrence.
The artifact has no internal GraphNest repository row ID: S1.03 must pass that
storage key separately. Equal names or original IDs do not collapse overloads
and distinct declarations. Occurrences must be unique within their collection;
edge and unresolved endpoints reference declaration occurrences.

Producers/importers must assign stable occurrences from source evidence, with
a discriminator for identical repeated evidence, rather than import order or
server row IDs. The test-only fixture converter demonstrates a content-derived
edge/reference occurrence plus a multiplicity ordinal, retaining the original
SQLite row ID separately. This is not a production import policy.

`SemanticHashV2` hashes deterministic protobuf of a canonical copy. It sorts
fact collections and namespaced extensions while preserving meaningful list
order (decorators, parameters, candidates and JSON arrays). JSON object keys and
exact decimal numbers are canonicalized without float64 rounding. Integers
above 2^53 remain exact; `1`, `1.0`, and `1e0` hash identically. Duplicate JSON
keys are rejected.

The hash excludes its own field, artifact import time, node update times, file
modification/index times, project-metadata update times, and edge/unresolved
database row IDs. Original node IDs, paths, evidence, producer configuration,
and stable public repository identity remain semantic. Unknown extension keys
are never guessed to be volatile. The wire roundtrip preserves the original
timestamps, row IDs, JSON encoding, and ordering.

An empty content hash is allowed while constructing an artifact. A supplied
hash must be 32 bytes and match the semantic digest. To replace semantic facts,
clear the old hash before recomputing it. This contract makes no publication
decision; storage/import stages must calculate and persist the semantic hash.

## Coordinates and exact source

Canonical positions use zero-based lines and UTF-16 code units, with exclusive
range ends. The pinned CodeGraph commit
`b9ca4b7981116909900368cc1686a1074cd4d4c1` emits one-based lines and zero-based
UTF-16 columns in both paths:

- Portable: `src/extraction/tree-sitter.ts` copies `startPosition.column` and
  `endPosition.column`, adding one to rows. `tree-sitter-helpers.ts` uses JS
  `substring` with syntax-node indices. The pinned `web-tree-sitter` binding
  (`lib/tree-sitter.c`) parses `TSInputEncodingUTF16LE` and applies
  `byte_to_code_unit` to emitted points.
- Native: Rust Tree-sitter starts with UTF-8 bytes. The kernel's
  `codegraph-kernel/src/textutil.rs::col16` counts `char.len_utf16()` from the
  line start to the byte offset before emitting columns. Language walkers,
  including `tsjs`, call this helper. `src/extraction/kernel/decode.ts` reads
  these converted columns unchanged.

Thus these producer records need line-origin conversion while retaining their
verified UTF-16 columns. Treating the resulting columns as Go byte offsets is
incorrect. `SourceOffset` resolves a complete canonical point against exact
UTF-8 source, rejects split surrogate pairs or out-of-line positions, preserves
CR before LF, and handles the final empty line. It does not guess missing
coordinates or normalize source text.

The committed `unicode.ts` fixture has CRLF plus accented/astral text before a
real call. The roundtrip test resolves the actual database call coordinate to
`normalize(` and proves its byte column differs from its UTF-16 column. Unit
tests cover scalar/surrogate boundaries, CRLF, EOF, and absent coordinates.
Native coordinate interpretation is source-verified here; this stage does not
claim a native-kernel runtime parity run.

## Untrusted input bounds

The v2 reader checks total encoded bytes and walks known protobuf descriptors
before allocating decoded messages. It bounds nodes, edges, files, unresolved
references, diagnostics, metadata entries, nested lists, strings and extension
bytes. Unknown fields, duplicate singular fields, scalar-width overflow and
invalid wire types are rejected. A future incompatible contract or unknown
version fails; `ErrUnsupportedVersion` also matches `ErrInvalidArtifact`.

V2 adds these defaults to `Limits`; callers may lower them within hard caps:

| Bound | Default | Hard cap |
| --- | --- | --- |
| Encoded/model bytes | 128 MiB | 256 MiB |
| Files, diagnostics | 100,000 each | 2,000,000 each |
| Unresolved references | 2,000,000 | 10,000,000 |
| Metadata/extension aggregate bytes | 4 MiB | 16 MiB |
| One extension | 64 KiB | 1 MiB |
| Other repeated field items | 1,024 | 4,096 |

Existing node/edge/path/identifier limits remain in force. Documentation and
diagnostic messages are capped at 256 KiB. The in-memory bound conservatively
accounts for protobuf tags/lengths/scalars. JSON trees have a depth limit of 32
and a token-value budget of 32,768; decimal exponents are bounded to one million
without allocating exponent-sized strings. Bounds are also checked for direct
in-memory callers before cloning, hashing, or marshaling.

## Verification

Tests read the actual committed `reference.db` through Python's standard
library (already used by `test/parity`), convert its five fact/evidence tables
to v2, roundtrip, then reconstruct and compare every original column. No oracle
facts are altered. The separate synthetic contract covers all 23 kinds and 13
relationships. Additional tests cover diagnostic/file-error metadata, missing
values, independent occurrences, exact decimals, hostile bounds, source
coordinates, and randomized ordering/timestamp/row-ID changes. Bounded fuzz
targets cover protobuf parsing, identities, and JSON canonicalization.

```sh
GOTOOLCHAIN=go1.26.6 GOWORK=off go test -race ./internal/graphartifact ./internal/graphingest ./internal/enrichment ./internal/postgres
GOTOOLCHAIN=go1.26.6 GOWORK=off go test ./internal/graphartifact -run '^$' -fuzz '^FuzzV2Parse$' -fuzztime=10s -parallel=2
GOTOOLCHAIN=go1.26.6 GOWORK=off go test ./internal/graphartifact -run '^$' -fuzz '^FuzzV2Identity$' -fuzztime=10s -parallel=2
GOTOOLCHAIN=go1.26.6 GOWORK=off go test ./internal/graphartifact -run '^$' -fuzz '^FuzzV2JSON$' -fuzztime=10s -parallel=2
GOTOOLCHAIN=go1.26.6 make tools-check
```

Generation uses the existing pinned tool workspace, with separate v1/v2 Buf
modules and import-based output paths. The v1 generated file stays identical.
`tools-check` requires a clean committed diff because it runs `git diff` after
generation. Buf's optional default lint also reports the existing
`PACKAGE_DIRECTORY_MATCH` convention mismatch for the v1 module layout; v2
uses the same layout. This stage does not move the established v1 descriptor.

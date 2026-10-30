# Spec Delta

## Purpose

Let administrators describe a security advisory once, in a small format that
can be pasted from a bulletin or an OSV record, and keep it as a versioned
record that GraphNest checks every repository against.

## ADDED Requirements

### Requirement: Advisory format
An advisory SHALL be YAML or JSON with a required `id` matching `^[A-Za-z0-9:_.-]{1,64}$`, a required `title`, a required `published` date, optional `aliases`, `summary` and `references`, and at least one product in `products` or one record in `osv`. Each product SHALL have a `name`, one or more `match` package URL patterns without versions, and version rules.

#### Scenario: Minimal advisory
- **WHEN** an administrator submits `id: CVE-2026-41827`, a title, `published: 2026-10-21`, and one product `{name: Node.js, match: [pkg:generic/node], fixed: [22.21.1]}`
- **THEN** the advisory is accepted

#### Scenario: Missing id
- **WHEN** an advisory has no `id`
- **THEN** it is rejected with `400 invalid_request` and the message names `id`

### Requirement: Version rules
A product SHALL state affected versions either as a `fixed` list, where each version closes its release line, or as explicit `affected` ranges of `introduced`/`fixed`/`last_affected` events. It MAY add `introduced`, a `line` precision of `major`, `minor`, or `patch`, `eol` line selectors such as `<20` or `21`, and `tag_aliases` mapping names such as `jod` to a line.

#### Scenario: Fixed shorthand
- **WHEN** a product has `fixed: [20.19.6, 22.21.1, 24.11.1]`
- **THEN** versions `22.0.0` up to but excluding `22.21.1` are affected, `22.21.1` and later 22.x releases are not, and lines without a fixed version and not listed under `eol` are treated as affected

#### Scenario: Explicit ranges
- **WHEN** a product has `affected: [{introduced: 1.0.9, fixed: 1.1.0}]`
- **THEN** only versions from `1.0.9` up to but excluding `1.1.0` are affected

### Requirement: Release lines are inferred and shown
Without an explicit `line`, the release-line precision SHALL be the fewest leading version components that tell all `fixed` versions apart, or a single line when there is one fixed version. The inferred precision for every product SHALL be shown in check results and the advisory detail.

#### Scenario: Minor lines inferred
- **WHEN** a product has `fixed: [1.35.8, 1.36.5, 1.37.3, 1.38.1]` and no `line`
- **THEN** its lines are minors (1.35 to 1.38) and the check result says so

#### Scenario: Explicit override
- **WHEN** a product has `fixed: [8.0.21]` and `line: minor`
- **THEN** 8.0.x versions below 8.0.21 are affected, 8.0.21 and later 8.0.x releases are not, and 9.0.x versions count as a line without a fixed version

### Requirement: OSV records are accepted
A plain OSV JSON record SHALL be accepted as the whole input or as an entry of `osv`. Each affected package SHALL become a product matched by its `purl`, or by a package URL derived from its ecosystem and name. `SEMVER` and `ECOSYSTEM` ranges and listed `versions` SHALL be used; other range types SHALL be skipped and reported.

#### Scenario: GHSA record for a PyPI package
- **WHEN** an OSV record lists `{ecosystem: PyPI, name: Brotli}` with a range `introduced 0`, `fixed 1.2.0`
- **THEN** a product matching `pkg:pypi/brotli` treats versions below `1.2.0` as affected

#### Scenario: Git range
- **WHEN** an OSV record contains a range of type `GIT`
- **THEN** that range is skipped and the check result reports it as skipped

### Requirement: Validation errors name the field
Invalid input SHALL be rejected with `400 invalid_request` and a message naming the field path and the offending value, such as `products[1].fixed[2]: "22.21" is not a full version`. Unknown fields SHALL be rejected.

#### Scenario: Bad version
- **WHEN** a product has `fixed: [22.21.1, "twenty"]`
- **THEN** the message names `products[0].fixed[1]` and the value `twenty`

#### Scenario: Unknown field
- **WHEN** a product has a misspelled key `fixd`
- **THEN** the message names `products[0].fixd` as unknown

#### Scenario: Unsupported package type
- **WHEN** a product matches `pkg:deb/debian/libbrotli1`
- **THEN** it is rejected with a message naming `products[0].match[0]` and stating that distribution package versions are not supported

### Requirement: Bounded and inert parsing
Advisory input SHALL be limited to 256 KiB and to bounded counts of products, patterns, versions, aliases, and OSV records. YAML anchors and aliases SHALL be rejected. URLs in `references` or OSV records SHALL be stored and displayed but never fetched.

#### Scenario: YAML alias
- **WHEN** an advisory uses a YAML anchor and alias
- **THEN** it is rejected with a message naming the alias

#### Scenario: Oversized input
- **WHEN** an advisory body exceeds 256 KiB
- **THEN** it is rejected with `413` before parsing

### Requirement: Dry-run check
Any principal allowed to read the supply-chain inventory SHALL be able to check an advisory definition without saving it. The check SHALL classify the principal's authorized repositories exactly as a saved advisory would and SHALL report products that matched nothing, tag names without an alias, skipped OSV ranges, and inferred release lines.

#### Scenario: Check before publishing
- **WHEN** an administrator checks a definition whose Node.js product sees the tag `gallium-slim` in three repositories
- **THEN** the result counts those repositories as unresolved and warns that `gallium-slim` has no tag alias, and nothing is saved

#### Scenario: Check scoped to the caller
- **WHEN** a user who can read 40 of 120 repositories runs a check
- **THEN** counts and rows cover only those 40 repositories

### Requirement: Advisories are versioned records
Creating, editing, closing, and reopening advisories SHALL be limited to administrators. The advisory `id` SHALL be unique and SHALL NOT change between versions. An edit SHALL create a new version that preserves the previous versions and their original input bytes. Closing SHALL stop re-checking and keep results and history readable.

#### Scenario: Edit adds a fixed version
- **WHEN** an administrator edits advisory `CVE-2026-41827` to add Envoy fixed versions
- **THEN** version 2 is current, version 1 remains readable with its original input, and every repository is re-checked against version 2

#### Scenario: Non-administrator write
- **WHEN** a non-administrator tries to create or edit an advisory
- **THEN** the request fails with `403 forbidden`

#### Scenario: Duplicate id
- **WHEN** an administrator creates an advisory whose `id` already exists
- **THEN** the request fails with `409 conflict` naming the existing advisory

### Requirement: Advisory visibility
Advisory definitions and their versions SHALL be readable by every principal allowed to read the supply-chain inventory. Delegation-only tokens SHALL be refused, as on every supply-chain route.

#### Scenario: Reader sees definitions
- **WHEN** a user with access to any repository lists advisories
- **THEN** every advisory's id, title, versions, and state are listed, with counts limited to that user's repositories

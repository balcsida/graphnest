---
name: grepnest-guide
description: Use when selecting or interpreting GrepNest code graph tools, request fields, statuses, confidence, boundaries, or commits.
---

# GrepNest Graph Guide

Use only `list_repositories`, `context`, `impact`, `trace`, and administrator-only `cypher`.

## Selectors and status

- Call `list_repositories` with optional `limit`; it has no name filter. Use the returned exact repository name or positive GitHub repository ID as `repo`.
- Omit `repo` only when exactly one repository is authorized. `branch`, when set, must be the indexed branch.
- `context` accepts exactly one of `uid` or `name`; narrow a name with `file_path` and `kind`.
- `found` supplies a result, `ambiguous` supplies `candidates` to retry by UID, and `not_found` means no matching symbol.
- `trace` additionally returns `no_path`; `cypher` returns `ok`.

## Graph evidence

Relationship kinds are `calls`, `references`, `extends`, and `implements`. Confidence is `0..1`; filter with `impact.min_confidence`, and report low-confidence edges rather than presenting them as certain.

Every graph response includes `commits`, the exact indexed snapshots behind the answer. `list_repositories` does not. `boundaries` identify excluded or incomplete graph areas and their reasons; never claim completeness across them. `impact.partial` or `cypher.truncated` also means incomplete output.

## Tool limits

| Tool | Required input | Key bounds |
|---|---|---|
| `context` | `uid` or `name` | `per_category_limit` defaults/caps at 100; paginate with `per_category_offset` |
| `impact` | `target_uid`, `direction` (`upstream` or `downstream`) | depth defaults 3, caps 32; limit caps 100; optional tests, confidence, relations |
| `trace` | `source_uid`, `target_uid` | depth defaults 10, caps 30 |
| `cypher` | read-only `statement` | administrators only; parameterize values; rows cap 100, bytes cap 262144 |

Do not use `cypher` as an ordinary-user fallback. Prefer the bounded graph tools.

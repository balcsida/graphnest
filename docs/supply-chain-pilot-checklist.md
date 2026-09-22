# Dependencies & Licenses pilot comparison checklist

Use this checklist to compare GraphNest's inventory against an existing
assessment of the **same repositories, builds, and artifacts** (for example
those already reviewed by a Code Insight team). It measures coverage and gaps;
it does not claim replacement parity, and it does not migrate proprietary
audit history. Fill it in per repository or artifact and keep the filled copy
with the pilot report.

## Setup (once)

- [ ] `GRAPHNEST_SUPPLY_CHAIN=true` on the server; migrations 033–036 applied.
- [ ] GitHub App has `Contents: read` on every pilot repository; the
      dependency graph is enabled on GHES for them.
- [ ] Registry routes configured only for the ecosystems in scope
      (`GRAPHNEST_SUPPLY_CHAIN_REGISTRY_*`), pointing at the organization's
      mirrors; `ALLOW_PRIVATE` set only where the mirror is internal.
- [ ] Upload and review grants issued to the pilot participants
      (`PUT /v1/supply-chain/upload-grants`, `PUT /v1/supply-chain/review/grants`).
- [ ] The organization policy authored and activated
      (`POST /v1/supply-chain/policies`); the example policy left inactive.

## Per repository / artifact

Record the identifiers first so every later row is reproducible:

| Field | Value |
| --- | --- |
| Repository (GitHub ID, name) | |
| Existing assessment reference (tool, report ID, date, exact commit or artifact digest it covered) | |
| GraphNest GitHub snapshot ID, `collected_at`, `subject_assurance` (always `unknown` for GHES) | |
| GraphNest import snapshot IDs (stream, `uploaded_by`, producer tool, `subject_assurance`) | |

### Component coverage

- [ ] Components in the existing assessment: ___ (unique coordinates).
- [ ] Components in the GraphNest GitHub snapshot: ___ occurrences /
      ___ unique coordinates (from `GET /v1/supply-chain/repositories/{id}`
      and the overview).
- [ ] Components in the GraphNest import snapshot(s): ___ / ___.
- [ ] Present in the existing assessment but absent from every GraphNest
      stream (list, with the likely reason: ecosystem not in the dependency
      graph, build-time only, container base image, vendored source):
- [ ] Present in GraphNest but absent from the existing assessment (list):
- [ ] Version disagreements for the same package (list `name`, theirs, ours,
      and which stream):
- [ ] Components without a purl or version in GraphNest (`without_purl`,
      `without_version` on the overview): ___ / ___ — are they identifiable
      in the existing assessment?
- [ ] Coverage warnings on the GraphNest snapshot (`warning_count`, codes):

Note: GHES exports describe the default branch at collection time, not a
commit or a release. Compare an artifact assessment only against an
**artifact** import stream (Syft/ORT of the same build), never against the
GitHub observation.

### License evidence

- [ ] Assessment counts on the GraphNest snapshot (`license_summary`):
      resolved ___, declared ___, conflict ___, unlicensed ___, unknown ___,
      not_applicable ___, unassessed ___.
- [ ] For a sample of ≥ 20 components (or all if fewer), compare the existing
      concluded license with GraphNest's assessed expression:
      agree ___ / disagree ___ / GraphNest unknown ___ / theirs unknown ___.
- [ ] For each disagreement, record the evidence GraphNest shows
      (`GET .../component?element=`: declarations, registry rows with
      `route`, `raw_value`, `parse_status`, `outcome`) and the basis of the
      existing conclusion (scan finding, curation, manual review).
- [ ] Components where the existing tool relied on source scanning that
      GraphNest does not perform (license files inside packages, headers,
      copyright): list them; these are expected GraphNest gaps.
- [ ] Registry lookups that failed (`not_found`, `unavailable`, `rejected`)
      and why (route not configured, private package, mirror outage).

### Unknowns, conflicts, and review

- [ ] Number of occurrences in the GraphNest review queue for the repository
      (`GET /v1/supply-chain/review/queue`), by verdict.
- [ ] Number of those already decided in the existing process; are the
      decisions reproducible as GraphNest decisions with reason and scope?
- [ ] Conflicts GraphNest surfaced that the existing process had not:
- [ ] Decisions in the existing process that GraphNest cannot represent
      (list the missing concept: e.g. product-level scope, obligation
      tracking, redistribution class):

### Workflow gaps observed

- [ ] Missing filters, views, or exports the pilot team needed:
- [ ] Time to answer "where is package X used?" via
      `find_component_repositories` / `/v1/supply-chain/components/{key}`
      versus the existing tool:
- [ ] Data the team needed from the original document that GraphNest only
      keeps in the preserved original (CycloneDX `evidence`, ORT curations):

## Summary for the pilot report

- Component coverage (GitHub stream vs existing): ___ % of the existing
  assessment's coordinates present; ___ additional coordinates surfaced.
- License evidence agreement on the sample: ___ %; unresolved by GraphNest:
  ___ %.
- Conflicts and unknowns newly surfaced: ___.
- Review workflow gaps: (list).
- Explicit non-goals confirmed: no source-license scanning, no vulnerability
  data, no obligation advice, no migration of the existing audit history.

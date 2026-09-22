-- Review workflows and license policies (ADR-0017, Milestone 5). Three
-- separate record kinds: evidence corrections / human conclusions,
-- versioned organization policies with evaluation results, and scoped usage
-- decisions (approve / reject / exception). Prior records are never edited;
-- a newer record supersedes an older one.

-- Versioned policies. A policy is immutable once created; a new version is a
-- new row. Rules are stored as JSON and evaluated over the SPDX expression
-- tree, never over a flattened bag of names.
create table supply_chain_policies (
    id bigint generated always as identity primary key,
    name varchar(128) not null,
    version integer not null check (version > 0),
    -- 'example' policies are fixtures shipped for demonstration; 'organization' are operator-authored.
    kind varchar(16) not null check (kind in ('example', 'organization')),
    rules jsonb not null,
    unknown_handling varchar(16) not null check (unknown_handling in ('review_required', 'prohibited')),
    description text not null default '',
    created_by varchar(256) not null,
    created_at timestamptz not null default now(),
    active boolean not null default false,
    unique (name, version)
);
-- At most one active organization policy.
create unique index supply_chain_policies_one_active on supply_chain_policies (active) where active;

-- Human license conclusions for exact coordinates (or one occurrence). A
-- conclusion is evidence of source 'human' in the evidence table plus this
-- linkage that records its basis so later evidence changes are detectable.
create table supply_chain_conclusions (
    id bigint generated always as identity primary key,
    ecosystem varchar(64) not null,
    namespace text not null default '',
    name text not null,
    version text not null,
    evidence_id bigint not null references supply_chain_license_evidence(id),
    evidence_fingerprint bytea not null check (octet_length(evidence_fingerprint) = 32),
    reviewer varchar(256) not null,
    reason text not null,
    created_at timestamptz not null default now(),
    superseded_by bigint references supply_chain_conclusions(id)
);
create index supply_chain_conclusions_coordinates on supply_chain_conclusions (ecosystem, namespace, name, version, id desc);

-- Policy evaluation results per occurrence per policy version; historical
-- results are retained.
create table supply_chain_policy_results (
    id bigint generated always as identity primary key,
    component_id bigint not null references supply_chain_components(id) on delete cascade,
    snapshot_id bigint not null references supply_chain_snapshots(id) on delete cascade,
    policy_id bigint not null references supply_chain_policies(id),
    evidence_fingerprint bytea check (evidence_fingerprint is null or octet_length(evidence_fingerprint) = 32),
    verdict varchar(16) not null check (verdict in ('approved', 'prohibited', 'review_required', 'unknown')),
    explanation text not null,
    evaluated_at timestamptz not null default now(),
    current boolean not null default true
);
create unique index supply_chain_policy_results_current on supply_chain_policy_results (component_id) where current;
create index supply_chain_policy_results_snapshot on supply_chain_policy_results (snapshot_id, verdict) where current;

-- Scoped usage decisions. Scope is the exact coordinates within one
-- repository (usage context). A decision records the policy version and
-- evidence fingerprint it was made against; a later evidence change does not
-- erase it but marks it stale for re-review.
create table supply_chain_decisions (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    ecosystem varchar(64) not null,
    namespace text not null default '',
    name text not null,
    version text not null,
    kind varchar(16) not null check (kind in ('approve', 'reject', 'exception')),
    policy_id bigint references supply_chain_policies(id),
    policy_verdict varchar(16) not null default '',
    evidence_fingerprint bytea not null check (octet_length(evidence_fingerprint) = 32),
    reviewer varchar(256) not null,
    reason text not null,
    usage_context text not null default '',
    expires_at timestamptz,
    created_at timestamptz not null default now(),
    superseded_by bigint references supply_chain_decisions(id),
    check (kind <> 'exception' or expires_at is not null)
);
create index supply_chain_decisions_scope on supply_chain_decisions (repository_id, ecosystem, namespace, name, version, id desc);
create index supply_chain_decisions_expiry on supply_chain_decisions (expires_at) where expires_at is not null and superseded_by is null;

-- Reviewer capability: repository-scoped like upload grants. Administrators
-- need no grant. Reviewers still need read access to the repository.
create table supply_chain_review_grants (
    repository_id bigint not null references repositories(id) on delete cascade,
    subject varchar(256) not null,
    granted_by varchar(256) not null,
    created_at timestamptz not null default now(),
    primary key (repository_id, subject)
);

-- Append-only review audit trail.
create table supply_chain_review_events (
    id bigint generated always as identity primary key,
    kind varchar(32) not null,
    actor varchar(256) not null,
    repository_id bigint,
    target text not null,
    detail jsonb not null default '{}',
    created_at timestamptz not null default now()
);
create trigger supply_chain_review_events_append_only
before update or delete or truncate on supply_chain_review_events
for each statement execute function reject_audit_event_mutation();

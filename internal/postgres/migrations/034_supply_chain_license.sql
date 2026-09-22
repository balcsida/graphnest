-- Exact-version license evidence (ADR-0017, Milestone 2). Evidence rows are
-- immutable: a metadata change becomes a new row, never an edit. Evidence is
-- keyed by coordinates and the registry route that produced it, so a private
-- registry's answer never mixes with a public one.
create table supply_chain_license_evidence (
    id bigint generated always as identity primary key,
    -- Evidence origin.
    source varchar(32) not null check (source in ('producer_declared', 'producer_concluded', 'registry_npm', 'registry_nuget', 'registry_maven', 'import', 'human')),
    -- Registry route name from configuration ('' for producer/import/human evidence).
    route varchar(128) not null default '',
    -- Exact coordinates the evidence applies to.
    ecosystem varchar(64) not null,
    namespace text not null default '',
    name text not null,
    version text not null,
    -- Artifact identity when the evidence is tied to verified bytes ('' for coordinate-level evidence).
    artifact_sha256 varchar(64) not null default '' check (artifact_sha256 = '' or artifact_sha256 ~ '^[0-9a-f]{64}$'),
    -- What was observed.
    raw_value text not null,
    raw_kind varchar(32) not null check (raw_kind in ('expression', 'expression_or_file', 'license_file', 'license_url', 'license_name', 'legacy_object', 'missing', 'sentinel')),
    parse_status varchar(32) not null check (parse_status in ('parsed', 'unknown_terms', 'no_assertion', 'none', 'unlicensed', 'invalid', 'not_applicable')),
    normalized_expression text not null default '',
    expression_tree jsonb,
    unknown_terms text[] not null default '{}',
    license_url text not null default '',
    license_file_name text not null default '',
    -- Raw metadata snippet the resolver used (bounded, never the whole artifact).
    detail jsonb not null default '{}',
    -- Provenance.
    resolver_version integer not null check (resolver_version > 0),
    license_list_version varchar(32) not null,
    content_sha256 bytea check (content_sha256 is null or octet_length(content_sha256) = 32),
    fetched_at timestamptz not null default now(),
    -- Negative results (not found, no license metadata) expire and are retried.
    expires_at timestamptz,
    outcome varchar(32) not null check (outcome in ('resolved', 'not_found', 'no_license_metadata', 'unavailable', 'rejected', 'too_large', 'malformed')),
    http_status integer,
    message text not null default ''
);
create index supply_chain_license_evidence_coordinates on supply_chain_license_evidence (ecosystem, namespace, name, version, route, id desc);
create index supply_chain_license_evidence_expiry on supply_chain_license_evidence (expires_at) where expires_at is not null;

-- Enrichment work: which coordinates need a lookup, leased like other jobs.
create table supply_chain_enrichment_jobs (
    id bigint generated always as identity primary key,
    ecosystem varchar(64) not null,
    namespace text not null default '',
    name text not null,
    version text not null,
    route varchar(128) not null,
    state varchar(16) not null check (state in ('queued', 'running', 'succeeded', 'failed', 'skipped')),
    attempt integer not null default 0 check (attempt >= 0),
    max_attempts integer not null default 3 check (max_attempts between 1 and 10),
    run_after timestamptz not null default now(),
    lease_owner varchar(128),
    lease_expires_at timestamptz,
    fence bigint not null default 0,
    error_code varchar(64) not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
create unique index supply_chain_enrichment_jobs_one_active on supply_chain_enrichment_jobs (ecosystem, namespace, name, version, route) where state in ('queued', 'running');
create index supply_chain_enrichment_jobs_claim on supply_chain_enrichment_jobs (state, run_after, id);

-- Derived per-occurrence assessment: which evidence rows apply to a snapshot
-- component and whether they agree. Rebuilt whenever evidence changes; the
-- underlying evidence is never edited.
create table supply_chain_component_assessments (
    component_id bigint primary key references supply_chain_components(id) on delete cascade,
    snapshot_id bigint not null references supply_chain_snapshots(id) on delete cascade,
    status varchar(32) not null check (status in ('unknown', 'declared', 'resolved', 'conflict', 'unlicensed', 'not_applicable', 'pending')),
    normalized_expression text not null default '',
    evidence_ids bigint[] not null default '{}',
    conflict_detail text not null default '',
    assessed_at timestamptz not null default now(),
    evidence_fingerprint bytea check (evidence_fingerprint is null or octet_length(evidence_fingerprint) = 32)
);
create index supply_chain_component_assessments_snapshot on supply_chain_component_assessments (snapshot_id, status);

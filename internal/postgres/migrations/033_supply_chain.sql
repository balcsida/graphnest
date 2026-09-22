-- Supply-chain inventory (ADR-0017). Every table cascades from repositories so
-- repository removal removes visibility immediately. Nothing here references
-- indexed_sha: inventory eligibility is repository authorization alone.

-- Original documents, byte-preserving. Identical bytes share one row.
create table supply_chain_documents (
    id bigint generated always as identity primary key,
    sha256 bytea not null unique check (octet_length(sha256) = 32),
    format varchar(32) not null check (format in ('spdx-2.3-json', 'cyclonedx-1.6-json')),
    media_type varchar(128) not null,
    byte_size bigint not null check (byte_size >= 0),
    body bytea not null,
    first_seen_at timestamptz not null default now()
);

-- Immutable published inventories. A snapshot is never updated after insert.
create table supply_chain_snapshots (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    document_id bigint not null references supply_chain_documents(id),
    producer varchar(16) not null check (producer in ('github', 'import')),
    subject varchar(16) not null check (subject in ('source', 'artifact')),
    stream_key varchar(200) not null,
    collected_at timestamptz not null,
    created_at_claimed timestamptz,
    producer_tool text not null default '',
    document_namespace text not null default '',
    document_name text not null default '',
    spdx_version varchar(32) not null default '',
    data_license varchar(128) not null default '',
    subject_revision char(40) check (subject_revision is null or subject_revision ~ '^[0-9a-f]{40}$'),
    subject_assurance varchar(32) not null check (subject_assurance in ('unknown', 'producer_asserted', 'verified')),
    root_element_ids text[] not null default '{}',
    parser_version integer not null check (parser_version > 0),
    component_count integer not null check (component_count >= 0),
    edge_count integer not null check (edge_count >= 0),
    warning_count integer not null check (warning_count >= 0),
    warnings jsonb not null default '[]',
    published_at timestamptz not null default now()
);
create index supply_chain_snapshots_stream on supply_chain_snapshots (repository_id, stream_key, id desc);

-- Component occurrences keep document-scoped identity. Occurrences are never
-- merged across snapshots or repositories; portfolio views aggregate at query
-- time over authorized latest snapshots.
create table supply_chain_components (
    id bigint generated always as identity primary key,
    snapshot_id bigint not null references supply_chain_snapshots(id) on delete cascade,
    ordinal integer not null check (ordinal >= 0),
    element_id text not null,
    name text not null,
    version text,
    purl text,
    ecosystem varchar(64),
    purl_namespace text,
    purl_name text,
    purl_version text,
    qualifiers jsonb not null default '{}',
    license_declared_raw text,
    license_concluded_raw text,
    download_location text,
    supplier text,
    checksums jsonb not null default '[]',
    is_root boolean not null default false,
    unique (snapshot_id, element_id)
);
create index supply_chain_components_coordinates on supply_chain_components (ecosystem, purl_name, version);
create index supply_chain_components_name on supply_chain_components (snapshot_id, lower(name), ordinal);

-- Preserved edges with their original direction and type. resolved=false marks
-- an edge whose endpoint is not a component of the snapshot (diagnostic, not a
-- dependency path).
create table supply_chain_relationships (
    id bigint generated always as identity primary key,
    snapshot_id bigint not null references supply_chain_snapshots(id) on delete cascade,
    from_element text not null,
    relationship varchar(64) not null,
    to_element text not null,
    resolved boolean not null
);
create index supply_chain_relationships_from on supply_chain_relationships (snapshot_id, from_element);
create index supply_chain_relationships_to on supply_chain_relationships (snapshot_id, to_element);

-- Durable refresh jobs, leased like index_jobs. fence increments on every
-- lease so a stale worker cannot publish over a newer lease.
create table supply_chain_jobs (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    stream_key varchar(200) not null,
    reason varchar(16) not null check (reason in ('scheduled', 'manual', 'webhook')),
    state varchar(16) not null check (state in ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'superseded')),
    priority integer not null default 0,
    attempt integer not null default 0 check (attempt >= 0),
    max_attempts integer not null default 5 check (max_attempts between 1 and 10),
    run_after timestamptz not null default now(),
    lease_owner varchar(128),
    lease_expires_at timestamptz,
    fence bigint not null default 0,
    requested_by varchar(256) not null default '',
    error_code varchar(64) not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
create unique index supply_chain_jobs_one_queued on supply_chain_jobs (repository_id, stream_key) where state = 'queued';
create unique index supply_chain_jobs_one_running on supply_chain_jobs (repository_id, stream_key) where state = 'running';
create index supply_chain_jobs_claim on supply_chain_jobs (state, run_after, priority desc, id);

-- One row per collection attempt, success or failure. Failures never touch
-- the stream's latest snapshot.
create table supply_chain_collections (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    job_id bigint references supply_chain_jobs(id) on delete set null,
    producer varchar(16) not null check (producer in ('github', 'import')),
    stream_key varchar(200) not null,
    started_at timestamptz not null,
    finished_at timestamptz not null,
    outcome varchar(16) not null check (outcome in ('published', 'unchanged', 'unavailable', 'forbidden', 'rate_limited', 'not_found', 'malformed', 'too_large', 'transient', 'cancelled', 'error')),
    http_status integer,
    retry_after_seconds integer,
    snapshot_id bigint references supply_chain_snapshots(id) on delete set null,
    document_id bigint references supply_chain_documents(id) on delete set null,
    error_code varchar(64) not null default '',
    message text not null default '',
    projection_error varchar(64) not null default ''
);
create index supply_chain_collections_stream on supply_chain_collections (repository_id, stream_key, id desc);

-- Current-stream pointers. latest_snapshot_id only moves forward on a
-- successful publication.
create table supply_chain_streams (
    repository_id bigint not null references repositories(id) on delete cascade,
    stream_key varchar(200) not null,
    producer varchar(16) not null check (producer in ('github', 'import')),
    subject varchar(16) not null check (subject in ('source', 'artifact')),
    latest_snapshot_id bigint references supply_chain_snapshots(id) on delete set null,
    latest_collection_id bigint references supply_chain_collections(id) on delete set null,
    last_success_at timestamptz,
    last_attempt_at timestamptz,
    last_outcome varchar(16) not null default '',
    opt_out boolean not null default false,
    updated_at timestamptz not null default now(),
    primary key (repository_id, stream_key)
);

-- Standards-based imports (ADR-0017, Milestone 4). The uploader's identity is
-- recorded separately from whatever producer the document claims, and the
-- subject binding is recorded only at the assurance level actually
-- established (an uploader's assertion is producer_asserted, never verified).
alter table supply_chain_snapshots
    add column uploaded_by varchar(256) not null default '',
    add column upload_label varchar(64) not null default '';

-- Import idempotency and quota: one row per accepted or rejected upload
-- attempt, keyed by document hash within a repository stream.
create table supply_chain_imports (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    stream_key varchar(200) not null,
    document_sha256 bytea not null check (octet_length(document_sha256) = 32),
    format varchar(32) not null check (format in ('spdx-2.3-json', 'cyclonedx-1.6-json')),
    uploaded_by varchar(256) not null,
    upload_label varchar(64) not null default '',
    byte_size bigint not null check (byte_size >= 0),
    outcome varchar(16) not null check (outcome in ('published', 'unchanged', 'rejected')),
    snapshot_id bigint references supply_chain_snapshots(id) on delete set null,
    error_code varchar(64) not null default '',
    message text not null default '',
    created_at timestamptz not null default now()
);
create index supply_chain_imports_stream on supply_chain_imports (repository_id, stream_key, id desc);
create index supply_chain_imports_quota on supply_chain_imports (repository_id, created_at desc);

-- Repository-scoped upload permission: which principals (users, by external
-- identity subject) may import into which repositories. Administrators need
-- no grant. Empty by default.
create table supply_chain_upload_grants (
    repository_id bigint not null references repositories(id) on delete cascade,
    subject varchar(256) not null,
    granted_by varchar(256) not null,
    created_at timestamptz not null default now(),
    primary key (repository_id, subject)
);

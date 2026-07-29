create table graph_uploads (
    id bigint generated always as identity primary key,
    repository_id bigint not null unique references repositories(id) on delete cascade,
    commit char(40) not null check (commit ~ '^[0-9a-f]{40}$'),
    schema_version integer not null check (schema_version = 1),
    source varchar(16) not null check (source in ('managed', 'external')),
    analyzer_name text not null,
    analyzer_version text not null,
    content_hash bytea not null check (octet_length(content_hash) = 32),
    node_count integer not null check (node_count >= 0),
    edge_count integer not null check (edge_count >= 0),
    uploaded_at timestamptz not null default now()
);

create table graph_nodes (
    id bigint generated always as identity primary key,
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    uid text not null,
    kind smallint not null check (kind between 1 and 3),
    path text not null,
    language text not null,
    symbol_kind text not null,
    qualified_name text not null,
    signature text not null,
    scip_symbol text not null,
    start_line integer not null check (start_line >= 0),
    start_character integer not null check (start_character >= 0),
    end_line integer not null check (end_line >= start_line),
    end_character integer not null check (end_character >= 0),
    constraint graph_nodes_range check (end_line > start_line or end_character >= start_character),
    constraint graph_nodes_upload_uid_unique unique (upload_id, uid)
);

create table graph_edges (
    id bigint generated always as identity primary key,
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    source_uid text not null,
    target_uid text not null,
    kind smallint not null check (kind between 1 and 6),
    path text not null,
    start_line integer not null check (start_line >= 0),
    start_character integer not null check (start_character >= 0),
    end_line integer not null check (end_line >= start_line),
    end_character integer not null check (end_character >= 0),
    confidence real not null check (confidence between 0 and 1),
    resolution_reason text not null,
    constraint graph_edges_range check (end_line > start_line or end_character >= start_character),
    constraint graph_edges_source foreign key (upload_id, source_uid)
        references graph_nodes(upload_id, uid) on delete cascade,
    constraint graph_edges_target foreign key (upload_id, target_uid)
        references graph_nodes(upload_id, uid) on delete cascade,
    constraint graph_edges_unique unique
        (upload_id, source_uid, target_uid, kind, path, start_line, start_character, end_line, end_character)
);

create table graph_jobs (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    target_sha char(40) not null check (target_sha ~ '^[0-9a-f]{40}$'),
    state varchar(32) not null check (state in ('queued', 'running', 'succeeded', 'failed', 'superseded')),
    attempt integer not null default 0 check (attempt >= 0 and attempt <= 5),
    max_attempts integer not null default 5 check (max_attempts between 1 and 5),
    run_after timestamptz not null default now(),
    lease_owner varchar(255),
    lease_expires_at timestamptz,
    error_code varchar(128),
    error_message varchar(1024),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    check (
        (state = 'running' and lease_owner is not null and lease_expires_at is not null)
        or (state <> 'running' and lease_owner is null and lease_expires_at is null)
    )
);

create unique index graph_jobs_one_queued on graph_jobs(repository_id) where state = 'queued';
create unique index graph_jobs_one_running on graph_jobs(repository_id) where state = 'running';
create index graph_jobs_claim on graph_jobs(run_after, id) where state = 'queued';
create index graph_jobs_reaper on graph_jobs(lease_expires_at, id) where state = 'running';

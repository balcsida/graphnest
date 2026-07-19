create sequence zoekt_repo_id_seq as bigint minvalue 1 maxvalue 4294967295 no cycle;

create table installations (
    id bigint generated always as identity primary key,
    github_id bigint not null unique check (github_id > 0),
    account_login varchar(255) not null,
    account_type varchar(32) not null,
    status varchar(32) not null check (status in ('active', 'suspended', 'deleted')),
    suspended_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table repositories (
    id bigint generated always as identity primary key,
    github_id bigint not null unique check (github_id > 0),
    installation_id bigint not null references installations(id),
    owner varchar(255) not null,
    name varchar(255) not null,
    clone_url text not null,
    web_url text not null,
    default_branch varchar(255) not null,
    private boolean not null,
    archived boolean not null,
    enabled boolean not null,
    desired_sha char(40) check (desired_sha is null or desired_sha ~ '^[0-9a-f]{40}$'),
    indexed_sha char(40) check (indexed_sha is null or indexed_sha ~ '^[0-9a-f]{40}$'),
    status varchar(32) not null check (status in ('pending', 'ready', 'failed', 'disabled')),
    error_code varchar(128),
    zoekt_repo_id bigint not null default nextval('zoekt_repo_id_seq') unique check (zoekt_repo_id between 1 and 4294967295),
    last_indexed_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (installation_id, owner, name)
);

create table webhook_deliveries (
    id bigint generated always as identity primary key,
    delivery_id varchar(128) not null unique,
    event_name varchar(128) not null,
    installation_id bigint references installations(id),
    received_at timestamptz not null default now(),
    processed_at timestamptz,
    state varchar(32) not null check (state in ('received', 'accepted', 'ignored', 'failed')),
    error_code varchar(128)
);

create table index_jobs (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id),
    target_sha char(40) not null check (target_sha ~ '^[0-9a-f]{40}$'),
    state varchar(32) not null check (state in ('queued', 'running', 'succeeded', 'failed', 'superseded')),
    attempt integer not null default 0 check (attempt >= 0 and attempt <= 5),
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

create table search_nodes (
    singleton boolean primary key default true check (singleton),
    node_id varchar(255) not null unique,
    base_url text not null,
    state varchar(32) not null check (state in ('active', 'unavailable')),
    capacity_weight integer not null check (capacity_weight > 0),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index index_jobs_one_queued on index_jobs(repository_id) where state = 'queued';
create unique index index_jobs_one_running on index_jobs(repository_id) where state = 'running';
create index index_jobs_claim on index_jobs(run_after, id) where state = 'queued';
create index index_jobs_reaper on index_jobs(lease_expires_at, id) where state = 'running';
create index webhook_deliveries_retention on webhook_deliveries(received_at);

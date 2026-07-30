create table users (
    id bigint generated always as identity primary key,
    external_id varchar(1024) not null unique,
    user_name varchar(320) not null,
    display_name varchar(256) not null default '',
    scim_active boolean not null default true,
    suspended_at timestamptz,
    source varchar(16) not null check (source in ('scim','local')),
    deleted_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
create unique index users_name_active on users (lower(user_name)) where deleted_at is null;
create table user_identities (
    user_id bigint not null references users(id) on delete cascade,
    issuer varchar(2048) not null,
    subject varchar(1024) not null,
    primary key (issuer, subject),
    unique (user_id, issuer)
);
create table user_roles (
    user_id bigint primary key references users(id) on delete cascade,
    administrator boolean not null check (administrator)
);
create table user_repository_grants (
    user_id bigint not null references users(id) on delete cascade,
    repository_id bigint not null references repositories(github_id) on delete cascade,
    primary key (user_id, repository_id)
);
create table auth_login_flows (
    state_hash bytea primary key check (octet_length(state_hash) = 32),
    browser_hash bytea not null check (octet_length(browser_hash) = 32),
    provider varchar(16) not null check (provider = 'oidc'),
    nonce varchar(1024) not null check (nonce <> ''),
    code_verifier varchar(128) not null check (code_verifier <> ''),
    return_to varchar(256) not null default '/' check (return_to = '/'),
    created_at timestamptz not null default now(),
    expires_at timestamptz not null check (expires_at > created_at)
);
create index auth_login_flows_expires_at_idx on auth_login_flows (expires_at);
create table auth_sessions (
    token_hash bytea primary key check (octet_length(token_hash)=32),
    user_id bigint not null references users(id) on delete cascade,
    provider varchar(16) not null check (provider='oidc'),
    created_at timestamptz not null,
    last_seen_at timestamptz not null,
    idle_expires_at timestamptz not null,
    expires_at timestamptz not null,
    revoked_at timestamptz,
    check (created_at <= last_seen_at and last_seen_at < idle_expires_at and idle_expires_at <= expires_at)
);
create index auth_sessions_expires_at_idx on auth_sessions (expires_at);

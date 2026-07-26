create table auth_login_flows (
    state_hash bytea primary key check (octet_length(state_hash) = 32),
    browser_hash bytea not null check (octet_length(browser_hash) = 32),
    provider text not null constraint auth_login_flows_provider_check check (provider = 'oidc'),
    nonce text not null, code_verifier text not null,
    return_to text not null default '/' check (return_to = '/'),
    created_at timestamptz not null default now(), expires_at timestamptz not null,
    constraint auth_login_flows_expiry_check check (expires_at > created_at),
    constraint auth_login_flows_oidc_fields_check check (nonce <> '' and code_verifier <> '')
);
create index auth_login_flows_expires_at_idx on auth_login_flows (expires_at);
create table auth_sessions (
    token_hash bytea primary key check (octet_length(token_hash) = 32),
    provider text not null constraint auth_sessions_provider_check check (provider = 'oidc'),
    principal_subject text not null, display_name text not null default '',
    method text not null check (method = provider),
    administrator boolean not null default false check (administrator = false),
    installation_id bigint not null check (installation_id > 0),
    repository_ids bigint[] not null check (cardinality(repository_ids) > 0),
    created_at timestamptz not null default now(), expires_at timestamptz not null,
    check (expires_at > created_at)
);
create index auth_sessions_expires_at_idx on auth_sessions (expires_at);

-- MCP OAuth authorization server: dynamically registered public clients, the
-- browser interactions that turn a login into an authorization code, and the
-- resulting grants whose access tokens authenticate MCP clients.
create table oauth_clients (
    id varchar(64) primary key check (id like 'gnc\_%'),
    client_name varchar(64) not null check (client_name <> ''),
    redirect_uris text[] not null check (cardinality(redirect_uris) between 1 and 8),
    created_at timestamptz not null default now(),
    last_used_at timestamptz not null default now()
);
create index oauth_clients_last_used_at_idx on oauth_clients (last_used_at);

create table oauth_authorization_requests (
    id bytea primary key check (octet_length(id) = 32),
    phase varchar(8) not null check (phase in ('pending','code')),
    client_id varchar(64) not null references oauth_clients(id) on delete cascade,
    user_id bigint references users(id) on delete cascade,
    redirect_uri varchar(2048) not null check (redirect_uri <> ''),
    code_challenge varchar(128) not null check (octet_length(code_challenge) between 43 and 128),
    state varchar(1024) not null default '',
    scope varchar(256) not null default '',
    resource varchar(2048) not null default '',
    created_at timestamptz not null default now(),
    expires_at timestamptz not null check (expires_at > created_at),
    check (phase = 'pending' or user_id is not null)
);
create index oauth_authorization_requests_expires_at_idx on oauth_authorization_requests (expires_at);

create table oauth_grants (
    id bigint generated always as identity primary key,
    client_id varchar(64) not null references oauth_clients(id) on delete cascade,
    user_id bigint not null references users(id) on delete cascade,
    scope varchar(256) not null default '',
    access_hash bytea not null unique check (octet_length(access_hash) = 32),
    access_expires_at timestamptz not null,
    refresh_hash bytea not null unique check (octet_length(refresh_hash) = 32),
    previous_refresh_hash bytea unique check (octet_length(previous_refresh_hash) = 32),
    github_token_ct bytea,
    created_at timestamptz not null default now(),
    last_used_at timestamptz not null default now(),
    expires_at timestamptz not null check (expires_at > created_at),
    revoked_at timestamptz
);
create index oauth_grants_user_id_idx on oauth_grants (user_id);
create index oauth_grants_expires_at_idx on oauth_grants (expires_at);

-- The GitHub login flow may now return to the authorization endpoint so a
-- pending MCP authorization can continue with consent.
alter table auth_login_flows drop constraint auth_login_flows_return_to_check;
alter table auth_login_flows
    add constraint auth_login_flows_return_to_check
    check (return_to in ('/', '/oauth/authorize/resume'));

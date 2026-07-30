create table groups (
    id bigint generated always as identity primary key,
    external_id varchar(1024) not null unique,
    display_name varchar(256) not null,
    deleted_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
create unique index groups_name_active on groups (lower(display_name)) where deleted_at is null;
create table group_memberships (
    group_id bigint not null references groups(id) on delete cascade,
    user_id bigint not null references users(id) on delete cascade,
    primary key (group_id,user_id)
);
create table group_roles (
    group_id bigint primary key references groups(id) on delete cascade,
    administrator boolean not null check (administrator)
);
create table group_repository_grants (
    group_id bigint not null references groups(id) on delete cascade,
    repository_id bigint not null references repositories(github_id) on delete cascade,
    primary key (group_id,repository_id)
);
create table api_tokens (
    id bigint generated always as identity primary key,
    token_hash bytea not null unique check (octet_length(token_hash)=32),
    prefix varchar(16) not null,
    user_id bigint not null references users(id) on delete cascade,
    repository_ids bigint[],
    created_at timestamptz not null default now(),
    last_used_at timestamptz,
    expires_at timestamptz,
    revoked_at timestamptz
);

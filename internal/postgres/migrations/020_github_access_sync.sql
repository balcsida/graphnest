-- GitHub-derived access: users provisioned just-in-time from GitHub OAuth and
-- repository grants mirrored from GitHub App user authorizations at login.
alter table users drop constraint users_source_check;
alter table users
    add constraint users_source_check
    check (source in ('scim','local','github'));

create table user_github_grants (
    user_id bigint not null references users(id) on delete cascade,
    repository_id bigint not null references repositories(github_id) on delete cascade,
    synced_at timestamptz not null default now(),
    primary key (user_id, repository_id)
);

create table password_credentials (
    user_id bigint primary key references users(id) on delete cascade,
    salt bytea not null check (octet_length(salt)=16),
    hash bytea not null check (octet_length(hash)=32),
    memory_kib integer not null check (memory_kib between 65536 and 262144),
    iterations integer not null check (iterations between 1 and 10),
    parallelism integer not null check (parallelism between 1 and 8),
    force_rotation boolean not null default true,
    updated_at timestamptz not null default now()
);
create table login_throttles (
    key_hash bytea primary key check (octet_length(key_hash)=32),
    failures integer not null check (failures > 0),
    window_started_at timestamptz not null,
    blocked_until timestamptz
);
create table audit_events (
    id bigint generated always as identity primary key,
    actor_type varchar(32) not null,
    actor_id varchar(128) not null default '',
    target_type varchar(32) not null,
    target_id varchar(128) not null default '',
    authentication_method varchar(32) not null default '',
    operation varchar(64) not null,
    outcome varchar(16) not null check (outcome in ('success','denied','invalid','error')),
    request_id varchar(128) not null default '',
    created_at timestamptz not null default now()
);
create index audit_events_created_id on audit_events (created_at desc,id desc);

create function reject_audit_event_mutation() returns trigger language plpgsql as $$
begin
    raise exception 'audit events are append-only';
end
$$;
create trigger audit_events_append_only
before update or delete on audit_events
for each statement execute function reject_audit_event_mutation();

alter table auth_sessions drop constraint auth_sessions_provider_check;
alter table auth_sessions add check (provider in ('oidc','local'));

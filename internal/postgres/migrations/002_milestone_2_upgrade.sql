alter table repositories add column if not exists size_bytes bigint not null default 0 check (size_bytes >= 0);

alter table webhook_deliveries alter column installation_id drop not null;

alter table index_jobs add column if not exists target_ref text not null default '';
alter table index_jobs add column if not exists reason varchar(128) not null default 'unspecified';
alter table index_jobs add column if not exists priority integer not null default 0;
alter table index_jobs add column if not exists max_attempts integer not null default 5 check (max_attempts between 1 and 5);
alter table index_jobs drop constraint if exists index_jobs_check;
alter table index_jobs add check (
    (state = 'running' and lease_owner is not null and lease_expires_at is not null)
    or (state <> 'running' and lease_owner is null and lease_expires_at is null)
);

drop index if exists index_jobs_claim;
create index index_jobs_claim on index_jobs(priority desc, run_after, id) where state = 'queued';

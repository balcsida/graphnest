alter table graph_jobs drop constraint graph_jobs_check;

alter table graph_jobs add constraint graph_jobs_lease_check check (
    (state = 'running' and lease_owner is not null)
    or
    (state <> 'running' and lease_owner is null and lease_expires_at is null)
);

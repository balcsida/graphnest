-- Repository-scoped graph publication permission: which principals (users, by
-- subject) may publish v2 graph generations to which repositories. Read access
-- never implies publication. Administrators need no grant. Empty by default.
create table graph_publication_grants (
    repository_id bigint not null references repositories(id) on delete cascade,
    subject varchar(256) not null,
    granted_by varchar(256) not null,
    created_at timestamptz not null default now(),
    primary key (repository_id, subject)
);

create table scip_uploads (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    commit char(40) not null,
    project_root text not null,
    indexer_name text not null,
    indexer_version text not null,
    uploaded_at timestamptz not null default now(),
    constraint scip_uploads_repository_unique unique (repository_id),
    constraint scip_uploads_commit_sha check (commit ~ '^[0-9a-f]{40}$')
);

create table scip_occurrences (
    id bigint generated always as identity primary key,
    upload_id bigint not null references scip_uploads(id) on delete cascade,
    path text not null,
    start_line integer not null,
    start_character integer not null,
    end_line integer not null,
    end_character integer not null,
    symbol text not null,
    roles integer not null,
    local boolean not null,
    constraint scip_occurrences_start_line_nonnegative check (start_line >= 0),
    constraint scip_occurrences_start_character_nonnegative check (start_character >= 0),
    constraint scip_occurrences_end_line_order check (end_line >= start_line),
    constraint scip_occurrences_end_character_nonnegative check (end_character >= 0),
    constraint scip_occurrences_range check (end_line > start_line or end_character >= start_character),
    constraint scip_occurrences_unique unique
        (upload_id, path, start_line, start_character, end_line, end_character, symbol)
);

create index scip_occurrences_position on scip_occurrences
    (upload_id, path, start_line, start_character, end_line, end_character);
create index scip_occurrences_symbol on scip_occurrences (symbol, local);

create table scip_relationships (
    id bigint generated always as identity primary key,
    upload_id bigint not null references scip_uploads(id) on delete cascade,
    source_symbol text not null,
    target_symbol text not null,
    is_definition boolean not null,
    is_reference boolean not null,
    is_implementation boolean not null,
    is_type_definition boolean not null,
    constraint scip_relationships_unique unique
        (upload_id, source_symbol, target_symbol, is_definition, is_reference, is_implementation, is_type_definition)
);

create index scip_relationships_source on scip_relationships (source_symbol);
create index scip_relationships_target on scip_relationships (target_symbol);

create table repository_packages (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    source varchar(32) not null,
    relation varchar(32) not null,
    purl text not null,
    manager text not null,
    name text not null,
    version text not null,
    constraint repository_packages_source check (source in ('manual', 'github')),
    constraint repository_packages_relation check (relation in ('provides', 'depends_on')),
    constraint repository_packages_unique unique (repository_id, source, relation, purl)
);

create index repository_packages_lookup on repository_packages (manager, name, version, relation);

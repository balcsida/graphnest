create table scip_uploads (
    id bigint generated always as identity primary key,
    repository_id bigint not null unique references repositories(id) on delete cascade,
    commit char(40) not null check (commit ~ '^[0-9a-f]{40}$'),
    project_root text not null,
    indexer_name text not null,
    indexer_version text not null,
    uploaded_at timestamptz not null default now()
);

create table scip_occurrences (
    id bigint generated always as identity primary key,
    upload_id bigint not null references scip_uploads(id) on delete cascade,
    path text not null,
    start_line integer not null check (start_line >= 0),
    start_character integer not null check (start_character >= 0),
    end_line integer not null check (end_line >= start_line),
    end_character integer not null check (end_character >= 0),
    symbol text not null,
    roles integer not null,
    local boolean not null,
    check (end_line > start_line or end_character >= start_character),
    unique (upload_id, path, start_line, start_character, end_line, end_character, symbol)
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
    unique (upload_id, source_symbol, target_symbol, is_definition, is_reference, is_implementation, is_type_definition)
);

create index scip_relationships_source on scip_relationships (source_symbol);
create index scip_relationships_target on scip_relationships (target_symbol);

create table repository_packages (
    id bigint generated always as identity primary key,
    repository_id bigint not null references repositories(id) on delete cascade,
    source varchar(32) not null check (source in ('manual', 'github')),
    relation varchar(32) not null check (relation in ('provides', 'depends_on')),
    purl text not null,
    manager text not null,
    name text not null,
    version text not null,
    unique (repository_id, source, relation, purl)
);

create index repository_packages_lookup on repository_packages (manager, name, version, relation);

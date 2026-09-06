-- Immutable generations retain query snapshots across replacement. Cleanup is
-- offline only, after draining readers and writers; see docs/graph-storage.md.
alter table graph_uploads drop constraint graph_uploads_repository_id_key;
alter table graph_uploads drop constraint graph_uploads_schema_version_check;
alter table graph_uploads add check (schema_version in (1, 2));
alter table graph_uploads add column active boolean not null default true;
alter table graph_uploads add column retired_at timestamptz;
alter table graph_uploads add column publisher text not null default 'legacy';
alter table graph_uploads add column capabilities text[] not null default '{}';
alter table graph_uploads add column public_repository text;
alter table graph_uploads add column producer_name bytea;
alter table graph_uploads add column producer_version bytea;
alter table graph_uploads add column producer_configuration bytea;
alter table graph_uploads add column artifact_header bytea;
alter table graph_uploads add constraint graph_uploads_v2_header check
    (schema_version = 1 or (public_repository is not null and producer_name is not null and producer_version is not null and producer_configuration is not null and artifact_header is not null));
create unique index graph_uploads_active_repository on graph_uploads(repository_id) where active;
create index graph_uploads_repository_generation on graph_uploads(repository_id, id);
create index graph_edges_incoming on graph_edges(upload_id, target_uid, kind, source_uid);
create index graph_edges_outgoing on graph_edges(upload_id, source_uid, kind, target_uid);

-- V2 keeps presence and producer evidence in generated protobuf messages; the
-- ordinary columns are the queryable projection, not a lossy replacement.
-- Protobuf strings can contain NUL and exceed B-tree tuple limits. Preserve
-- their UTF-8 bytes; only fixed-size hashes enter indexes/FKs. Queries also
-- compare original bytes, so hash equality alone never aliases facts.
create table graph_v2_nodes (
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    occurrence bytea not null,
    occurrence_key bytea generated always as (sha256(occurrence)) stored,
    ordinal integer not null check (ordinal >= 0),
    kind text not null check (kind in ('repository','symbol','file','module','class','struct','interface','trait','protocol','function','method','property','field','variable','constant','enum','enum_member','type_alias','namespace','parameter','import','export','route','component','union')),
    name bytea not null,
    qualified_name bytea not null,
    path bytea,
    language bytea not null,
    visibility bytea,
    is_exported boolean,
    payload bytea not null,
    primary key (upload_id, occurrence_key),
    unique (upload_id, ordinal)
);
create index graph_v2_nodes_name on graph_v2_nodes(upload_id, sha256(name), occurrence_key);
create index graph_v2_nodes_qualified_name on graph_v2_nodes(upload_id, sha256(qualified_name), occurrence_key);
create index graph_v2_nodes_path on graph_v2_nodes(upload_id, sha256(path), occurrence_key);
create index graph_v2_nodes_kind on graph_v2_nodes(upload_id, kind, occurrence_key);
create index graph_v2_nodes_visibility on graph_v2_nodes(upload_id, sha256(visibility), occurrence_key);
create index graph_v2_nodes_exported on graph_v2_nodes(upload_id, is_exported, occurrence_key);
create table graph_v2_edges (
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    occurrence bytea not null,
    occurrence_key bytea generated always as (sha256(occurrence)) stored,
    ordinal integer not null check (ordinal >= 0),
    source bytea not null,
    source_key bytea generated always as (sha256(source)) stored,
    target bytea not null,
    target_key bytea generated always as (sha256(target)) stored,
    kind smallint not null check (kind between 1 and 13),
    confidence double precision check (confidence between 0 and 1),
    payload bytea not null,
    primary key (upload_id, occurrence_key),
    unique (upload_id, ordinal),
    foreign key (upload_id, source_key) references graph_v2_nodes(upload_id, occurrence_key),
    foreign key (upload_id, target_key) references graph_v2_nodes(upload_id, occurrence_key)
);
create index graph_v2_edges_outgoing on graph_v2_edges(upload_id, source_key, kind, target_key, occurrence_key);
create index graph_v2_edges_incoming on graph_v2_edges(upload_id, target_key, kind, source_key, occurrence_key);
create table graph_v2_files (
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    path bytea not null,
    path_key bytea generated always as (sha256(path)) stored,
    ordinal integer not null check (ordinal >= 0),
    content_hash text not null check (content_hash ~ '^[0-9a-f]{64}$'),
    language bytea not null,
    size bigint not null check (size >= 0),
    generated boolean,
    payload bytea not null,
    primary key (upload_id, path_key),
    unique (upload_id, ordinal)
);
create table graph_v2_unresolved (
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    occurrence bytea not null,
    occurrence_key bytea generated always as (sha256(occurrence)) stored,
    ordinal integer not null check (ordinal >= 0),
    source bytea not null,
    source_key bytea generated always as (sha256(source)) stored,
    kind text not null check (kind in ('contains','imports','references','calls','extends','implements','exports','type_of','returns','instantiates','overrides','decorates','navigates','function_ref')),
    path bytea,
    payload bytea not null,
    primary key (upload_id, occurrence_key),
    unique (upload_id, ordinal),
    foreign key (upload_id, source_key) references graph_v2_nodes(upload_id, occurrence_key)
);
create index graph_v2_unresolved_source on graph_v2_unresolved(upload_id, source_key, kind, occurrence_key);
create table graph_v2_diagnostics (
    upload_id bigint not null references graph_uploads(id) on delete cascade,
    occurrence bytea not null,
    occurrence_key bytea generated always as (sha256(occurrence)) stored,
    ordinal integer not null check (ordinal >= 0),
    payload bytea not null,
    primary key (upload_id, occurrence_key),
    unique (upload_id, ordinal)
);

-- Enforce the version boundary even for direct SQL/COPY callers.
alter table graph_uploads add unique (id, schema_version);
alter table graph_nodes add column schema_version integer not null default 1 check (schema_version=1);
alter table graph_nodes add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_edges add column schema_version integer not null default 1 check (schema_version=1);
alter table graph_edges add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_v2_nodes add column schema_version integer not null default 2 check (schema_version=2);
alter table graph_v2_nodes add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_v2_edges add column schema_version integer not null default 2 check (schema_version=2);
alter table graph_v2_edges add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_v2_files add column schema_version integer not null default 2 check (schema_version=2);
alter table graph_v2_files add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_v2_unresolved add column schema_version integer not null default 2 check (schema_version=2);
alter table graph_v2_unresolved add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;
alter table graph_v2_diagnostics add column schema_version integer not null default 2 check (schema_version=2);
alter table graph_v2_diagnostics add foreign key (upload_id,schema_version) references graph_uploads(id,schema_version) on delete cascade;

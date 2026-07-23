-- Existing SCIP rows lack document encodings and relationship paths, so they
-- cannot be upgraded safely. SCIP data is derived and must be uploaded again.
delete from scip_uploads;

alter table scip_occurrences
    add column position_encoding smallint not null,
    add constraint scip_occurrences_position_encoding
        check (position_encoding between 1 and 3);

alter table scip_relationships
    add column document_path text not null,
    add column source_global_symbol_key bytea;

alter table scip_relationships drop constraint scip_relationships_unique;
alter table scip_relationships add constraint scip_relationships_unique unique
    (upload_id, document_path, source_symbol, target_symbol,
     is_definition, is_reference, is_implementation, is_type_definition);

drop index scip_occurrences_symbol;
create index scip_occurrences_symbol_lookup on scip_occurrences
    (symbol, upload_id, path);

drop index scip_relationships_source;
create index scip_relationships_source_lookup on scip_relationships
    (source_symbol, upload_id, document_path);

drop index scip_relationships_target;
create index scip_relationships_target_lookup on scip_relationships
    (target_symbol, upload_id, document_path);

create index scip_relationships_source_global_symbol_key on scip_relationships
    (source_global_symbol_key, upload_id)
    where source_global_symbol_key is not null;

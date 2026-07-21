alter table scip_occurrences add column global_symbol_key bytea;
create index scip_occurrences_global_symbol_key on scip_occurrences (global_symbol_key, upload_id)
    where global_symbol_key is not null;

alter table scip_relationships add column target_global_symbol_key bytea;
create index scip_relationships_target_global_symbol_key on scip_relationships (target_global_symbol_key, upload_id)
    where target_global_symbol_key is not null;

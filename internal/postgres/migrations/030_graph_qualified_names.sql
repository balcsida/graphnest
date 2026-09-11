-- Original qualified names remain byte-safe and rebuildable from v2 facts.
alter table graph_v2_discovery add column original_qualified_name bytea;
create index graph_v2_discovery_qualified_name on graph_v2_discovery(upload_id,sha256(original_qualified_name));

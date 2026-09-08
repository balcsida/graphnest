-- Original-byte selector and name-segment projections. Version 1 uploads keep
-- serving Discover; exact variants require an explicit atomic discovery rebuild.
alter table graph_v2_discovery
 add column original_name bytea,
 add column folded_name bytea,
 add column name_size integer,
 add column selector_grams text[],
 add column segments text[];
create index graph_v2_discovery_prefix on graph_v2_discovery(upload_id,substring(original_name from 1 for 128));
create index graph_v2_discovery_selector_grams on graph_v2_discovery using gin(selector_grams);
create index graph_v2_discovery_segments on graph_v2_discovery using gin(segments);

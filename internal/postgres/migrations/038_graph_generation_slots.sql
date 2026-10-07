-- One active generation per repository and artifact version. The v1
-- generation serves context, impact and trace; the v2 generation serves the
-- entity, discovery, exploration and symbol workflows. A SCIP upload publishes
-- both, so neither family of tools depends on the other's producer.
drop index graph_uploads_active_repository;
create unique index graph_uploads_active_repository on graph_uploads(repository_id, schema_version) where active;

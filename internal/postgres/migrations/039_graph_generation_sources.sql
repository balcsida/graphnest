-- Two active v2 slots per repository: the generation a publisher uploads
-- (source external) and the one GraphNest derives from a SCIP upload (source
-- scip) no longer compete. V1 keeps one active generation per repository.
-- Supersedes the index migration 038 created.
drop index graph_uploads_active_repository;
create unique index graph_uploads_active_v1 on graph_uploads(repository_id) where active and schema_version=1;
create unique index graph_uploads_active_v2 on graph_uploads(repository_id, source) where active and schema_version=2;

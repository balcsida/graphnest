alter table graph_uploads add constraint graph_uploads_manifest_id
    check (id between 1 and 4611686018427387903);
alter table scip_uploads add constraint scip_uploads_manifest_id
    check (id between 1 and 4611686018427387903);

alter sequence graph_uploads_id_seq maxvalue 4611686018427387903;
alter sequence scip_uploads_id_seq maxvalue 4611686018427387903;

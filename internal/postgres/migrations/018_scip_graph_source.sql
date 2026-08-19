alter table graph_uploads drop constraint graph_uploads_source_check;
alter table graph_uploads add constraint graph_uploads_source_check
    check (source in ('managed', 'external', 'scip'));

alter table webhook_deliveries
    add column repository_id bigint references repositories(id) on delete set null;

create index webhook_deliveries_repository_received
    on webhook_deliveries(repository_id, received_at desc, id desc);

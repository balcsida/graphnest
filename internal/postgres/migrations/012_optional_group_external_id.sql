alter table groups drop constraint if exists groups_external_id_key;
create unique index groups_external_id_active
    on groups (external_id)
    where external_id <> '' and deleted_at is null;

-- API token audit events record which kind of token they are about (ordinary,
-- delegated, delegation_only, inventory_only). Events from before this
-- migration keep an empty kind. Like the other descriptive columns, the value
-- is validated by the application, so adding the column does not scan the table.
alter table audit_events add column token_kind varchar(32) not null default '';

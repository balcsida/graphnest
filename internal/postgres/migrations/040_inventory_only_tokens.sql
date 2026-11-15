-- An inventory-only administrator API token lists every active repository and
-- reads its status, and is refused everywhere else. A token has one purpose at
-- most, so it can never be delegation-only as well. It is stored with an empty
-- ceiling rather than none, so that read without its marker (by a server from
-- before this migration) it matches no repository instead of every grant its
-- owner holds. Existing tokens are unmarked, satisfy both constraints, and
-- behave exactly as before.
alter table api_tokens
    add column inventory_only boolean not null default false,
    add constraint api_tokens_single_purpose check (not (delegation_only and inventory_only)),
    add constraint api_tokens_inventory_only_empty_ceiling
        check (not inventory_only or (repository_ids is not null and cardinality(repository_ids) = 0));

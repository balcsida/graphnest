-- A delegation-only administrator API token mints narrowed, short-lived tokens
-- for any active repository and is refused everywhere else. Existing tokens
-- keep their repository ceiling and behave exactly as before.
alter table api_tokens add column delegation_only boolean not null default false;

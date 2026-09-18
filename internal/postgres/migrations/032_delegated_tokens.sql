-- A token minted by the delegation endpoint is marked so it can never delegate
-- in turn: delegation is one generation deep, and a stolen child cannot be
-- rotated past its own expiry. Existing tokens are unmarked and behave as
-- before.
alter table api_tokens add column delegated boolean not null default false;

-- Retain every consumed refresh token until its grant is deleted. Each token's
-- grace deadline starts when it is consumed, independently of access traffic.
create table oauth_refresh_tokens (
    refresh_hash bytea primary key check (octet_length(refresh_hash) = 32),
    grant_id bigint not null references oauth_grants(id) on delete cascade,
    consumed_at timestamptz not null
);
create index oauth_refresh_tokens_grant_id_idx on oauth_refresh_tokens(grant_id);

-- Older rotations discarded their token history and exact rotation times.
-- Require those clients to authorize again to restore full replay protection.
update oauth_grants set revoked_at=coalesce(revoked_at, now()), github_token_ct=null
where previous_refresh_hash is not null;

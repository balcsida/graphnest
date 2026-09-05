-- Shared minute budgets bound both OAuth processing and limiter storage.
create table oauth_request_limits (
    endpoint text not null check (endpoint in ('/oauth/register', '/oauth/token')),
    source_hash bytea not null check (octet_length(source_hash) in (0, 32)),
    window_start timestamptz not null,
    request_count integer not null check (request_count > 0),
    primary key (endpoint, source_hash)
);

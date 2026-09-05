alter table oauth_request_limits
    drop constraint oauth_request_limits_endpoint_check,
    add constraint oauth_request_limits_endpoint_check
        check (endpoint in ('/oauth/register', '/oauth/authorize', '/oauth/token', '/oauth/revoke'));

alter table auth_login_flows drop constraint auth_login_flows_return_to_check;
alter table auth_login_flows
    add constraint auth_login_flows_return_to_check
    check (
        return_to in ('/', '/oauth/authorize/resume')
        or return_to ~ '^/oauth/authorize/resume\?request_id=[0-9a-f]{64}$'
    );

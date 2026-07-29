alter table users
    add column scim_name jsonb not null default '{}',
    add column scim_emails jsonb not null default '[]';

create sequence auth_session_audit_id_seq;
alter table auth_sessions add column audit_id varchar(128);
with ranked as (
    select token_hash, row_number() over (order by encode(token_hash, 'hex')) as position
    from auth_sessions
)
update auth_sessions
set audit_id = 'legacy-' || lpad(ranked.position::text, 20, '0')
from ranked
where auth_sessions.token_hash = ranked.token_hash;
alter table auth_sessions
    alter column audit_id set default
        ('session-' || lpad(nextval('auth_session_audit_id_seq')::text, 20, '0')),
    alter column audit_id set not null,
    add unique (audit_id);
alter sequence auth_session_audit_id_seq owned by auth_sessions.audit_id;

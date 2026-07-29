drop trigger audit_events_append_only on audit_events;
create trigger audit_events_append_only
before update or delete or truncate on audit_events
for each statement execute function reject_audit_event_mutation();

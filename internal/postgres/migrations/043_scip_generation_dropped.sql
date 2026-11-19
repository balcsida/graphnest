-- Why the v2 generation derived from this upload was not stored (too large
-- for the configured limits, or rejected as invalid); null when it was. The
-- repository's graph status reports it. Adding a nullable column without a
-- default does not rewrite the table.
alter table scip_uploads add column generation_dropped text;

-- Portfolio views group and page component occurrences by coordinate
-- (ecosystem, namespace, name, version). Without an index in that order every
-- component page sorted and aggregated every latest-snapshot occurrence in
-- scope. The expression is the portfolio's coordinate key and must stay
-- identical to coordinateKey in supply_chain_portfolio.go; the "C" collation
-- makes its order bytewise, which is what a keyset cursor needs and is several
-- times cheaper to compare than the database collation. snapshot_id follows
-- the key so a scan keeps only the latest snapshots in scope without visiting
-- the rows of retained history.
create index supply_chain_components_coordinate_key on supply_chain_components (
    ((coalesce(ecosystem, '') || chr(1) || coalesce(purl_namespace, '') || chr(1) || coalesce(purl_name, name) || chr(1) || coalesce(purl_version, version, '')) collate "C"),
    snapshot_id
);

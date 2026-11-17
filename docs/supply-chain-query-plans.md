# Supply-chain portfolio query plans

Recorded by `TestSupplyChainPortfolioQueryPlans` (`go test -tags=integration
./internal/postgres -run TestSupplyChainPortfolioQueryPlans` with
`GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS=<file>`) on 2026-11-17 against PostgreSQL
18.6 in Docker (OrbStack, Apple Silicon, container limited to one CPU). The
test explains the statements the store runs, not copies of them. Times are
measurements of this dataset in this environment, not a latency commitment.

Reading: every portfolio statement takes the snapshots in scope (the stream's
latest snapshot of each authorized repository) as `$1` and groups by the
coordinate key that migration 042 indexes in `"C"` collation
(`supply_chain_components_coordinate_key`).

- A component page reads occurrences from that index in key order, with the
  scope as part of the index condition, aggregates them coordinate by
  coordinate and stops when the page is full. Its cost follows the occurrences
  of the coordinates it returns, not the inventory, and it never visits the
  occurrences of retained history or of repositories outside the scope.
- A page filtered by search, license or assessment has to find the matches
  first, so it reads the occurrences in scope once, sorts the matching ones and
  aggregates the page. Assessments are joined within the scope only.
- The overview counts coordinates in one pass (hash aggregate by key, folded
  per ecosystem) and reads assessment counts from the `(snapshot_id, status)`
  index alone. It used to sort every occurrence by four text columns in the
  database collation, which was most of its time.
- The dependency graph is one pass that ranks every package, then reads
  assessments for the occurrences of the returned packages only.

## A large inventory

The same statements on a synthetic inventory of the size at which v0.13.0
timed out: 3,000 repositories, 900,000 occurrences in the latest snapshots
over about 59,500 unique coordinates, 100 of which every repository uses, and
four retained earlier snapshots per repository (4,500,000 component rows).
PostgreSQL 18.6 limited to one CPU, default settings (`work_mem` 4 MB), warm
cache, whole-inventory scope, best of three:

| Request | v0.13.0 | Now |
| --- | --- | --- |
| Component page, no filter or ecosystem only | 3.2 s | 10 to 15 ms |
| Component page of 100 coordinates that every repository uses | 3.2 s, then `500` for its size | 0.4 s |
| Component page filtered by assessment, license or search | 3.2 s or more | 0.8 to 1.9 s |
| Overview | 5.6 s in four statements | 0.9 s |
| Facets | 3.3 s in three statements | 0.75 s |
| Dependency graph | up to 30 component pages, 96 s | 0.6 to 1.4 s in one request |

A scope of 300 of those repositories answers every request in 0.2 s or less,
and a scope of five in a few milliseconds.

## The recorded dataset

Dataset: 200 repositories x 250 components = 50000 occurrences, ~5000 unique coordinates; seeded in 2.165s.
PostgreSQL: PostgreSQL 18.6 (Debian 18.6-1.pgdg13+2) on aarch64-unknown-linux-gnu, compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit

## overview counts

```
Aggregate  (cost=31.57..31.58 rows=1 width=24) (actual time=0.103..0.103 rows=1.00 loops=1)
  Buffers: shared hit=17
  ->  Hash Left Join  (cost=26.50..29.57 rows=200 width=20) (actual time=0.060..0.096 rows=200.00 loops=1)
        Hash Cond: (st.latest_snapshot_id = snap.id)
        Buffers: shared hit=17
        ->  Hash Left Join  (cost=9.00..11.54 rows=200 width=8) (actual time=0.031..0.055 rows=200.00 loops=1)
              Hash Cond: (r.id = st.repository_id)
              Buffers: shared hit=4
              ->  Function Scan on unnest r  (cost=0.00..2.00 rows=200 width=8) (actual time=0.008..0.012 rows=200.00 loops=1)
              ->  Hash  (cost=6.50..6.50 rows=200 width=16) (actual time=0.022..0.023 rows=200.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 18kB
                    Buffers: shared hit=4
                    ->  Seq Scan on supply_chain_streams st  (cost=0.00..6.50 rows=200 width=16) (actual time=0.003..0.013 rows=200.00 loops=1)
                          Filter: ((stream_key)::text = 'github:source'::text)
                          Buffers: shared hit=4
        ->  Hash  (cost=15.00..15.00 rows=200 width=20) (actual time=0.028..0.028 rows=200.00 loops=1)
              Buckets: 1024  Batches: 1  Memory Usage: 19kB
              Buffers: shared hit=13
              ->  Seq Scan on supply_chain_snapshots snap  (cost=0.00..15.00 rows=200 width=20) (actual time=0.001..0.018 rows=200.00 loops=1)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=16 read=1
Planning Time: 0.103 ms
Execution Time: 0.116 ms
```

Wall time including plan output: 1ms

## coordinates per ecosystem (overview)

```
HashAggregate  (cost=2918.53..2922.03 rows=200 width=136) (actual time=13.500..13.502 rows=6.00 loops=1)
  Group Key: min((COALESCE(c.ecosystem, ''::character varying))::text)
  Batches: 1  Memory Usage: 32kB
  Buffers: shared hit=1000
  ->  HashAggregate  (cost=2706.87..2818.27 rows=4456 width=88) (actual time=12.796..13.116 rows=4560.00 loops=1)
        Group Key: ((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text
        Batches: 1  Memory Usage: 785kB
        Buffers: shared hit=1000
        ->  Seq Scan on supply_chain_components c  (cost=0.50..2230.18 rows=31779 width=73) (actual time=0.007..7.672 rows=50200.00 loops=1)
              Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
              Buffers: shared hit=1000
Planning:
  Buffers: shared hit=95
Planning Time: 0.175 ms
Execution Time: 13.522 ms
```

Wall time including plan output: 14ms

## assessment counts (overview and facets)

```
HashAggregate  (cost=0.50..0.51 rows=1 width=90) (actual time=0.002..0.002 rows=0.00 loops=1)
  Group Key: status
  Batches: 1  Memory Usage: 32kB
  ->  Seq Scan on supply_chain_component_assessments  (cost=0.50..0.50 rows=1 width=82) (actual time=0.001..0.001 rows=0.00 loops=1)
        Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
Planning:
  Buffers: shared hit=15 read=2
Planning Time: 0.075 ms
Execution Time: 0.009 ms
```

Wall time including plan output: 1ms

## portfolio page (first 100)

```
Limit  (cost=2.67..214.05 rows=101 width=328) (actual time=0.283..0.648 rows=101.00 loops=1)
  Buffers: shared hit=550
  ->  GroupAggregate  (cost=2.67..9328.91 rows=4456 width=328) (actual time=0.282..0.644 rows=101.00 loops=1)
        Group Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text)
        Buffers: shared hit=550
        ->  Incremental Sort  (cost=2.67..8287.89 rows=31779 width=262) (actual time=0.210..0.437 rows=528.00 loops=1)
              Sort Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text) COLLATE "C", a.status
              Presorted Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text)
              Full-sort Groups: 11  Sort Method: quicksort  Average Memory: 28kB  Peak Memory: 28kB
              Pre-sorted Groups: 1  Sort Method: quicksort  Average Memory: 34kB  Peak Memory: 34kB
              Buffers: shared hit=550
              ->  Nested Loop Left Join  (cost=0.92..7351.19 rows=31779 width=262) (actual time=0.023..0.346 rows=540.00 loops=1)
                    Join Filter: (a.component_id = c.id)
                    Buffers: shared hit=550
                    ->  Index Scan using supply_chain_components_coordinate_key on supply_chain_components c  (cost=0.41..6397.31 rows=31779 width=124) (actual time=0.020..0.243 rows=540.00 loops=1)
                          Index Cond: ((((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > ''::text) AND (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                          Index Searches: 1
                          Buffers: shared hit=550
                    ->  Materialize  (cost=0.50..0.51 rows=1 width=122) (actual time=0.000..0.000 rows=0.00 loops=540)
                          Storage: Memory  Maximum Storage: 17kB
                          ->  Seq Scan on supply_chain_component_assessments a  (cost=0.50..0.50 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
                                Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
Planning:
  Buffers: shared hit=28
Planning Time: 0.222 ms
Execution Time: 0.669 ms
```

Wall time including plan output: 2ms

## portfolio page (100 from the middle)

```
Limit  (cost=2.23..154.96 rows=101 width=328) (actual time=0.069..1.497 rows=101.00 loops=1)
  Buffers: shared hit=1613
  ->  GroupAggregate  (cost=2.23..6633.29 rows=4385 width=328) (actual time=0.068..1.493 rows=101.00 loops=1)
        Group Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text)
        Buffers: shared hit=1613
        ->  Incremental Sort  (cost=2.23..6044.04 rows=15448 width=262) (actual time=0.060..1.067 rows=1555.00 loops=1)
              Sort Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text) COLLATE "C", a.status
              Presorted Key: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text)
              Full-sort Groups: 36  Sort Method: quicksort  Average Memory: 30kB  Peak Memory: 30kB
              Buffers: shared hit=1613
              ->  Nested Loop Left Join  (cost=0.92..5622.91 rows=15448 width=262) (actual time=0.021..0.801 rows=1601.00 loops=1)
                    Join Filter: (a.component_id = c.id)
                    Buffers: shared hit=1613
                    ->  Index Scan using supply_chain_components_coordinate_key on supply_chain_components c  (cost=0.41..5158.97 rows=15448 width=124) (actual time=0.019..0.504 rows=1601.00 loops=1)
                          Index Cond: ((((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > 'npm\x01\x01pkg-2500\x017.0.1'::text) AND (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                          Index Searches: 1
                          Buffers: shared hit=1613
                    ->  Materialize  (cost=0.50..0.51 rows=1 width=122) (actual time=0.000..0.000 rows=0.00 loops=1601)
                          Storage: Memory  Maximum Storage: 17kB
                          ->  Seq Scan on supply_chain_component_assessments a  (cost=0.50..0.50 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
                                Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
Planning:
  Buffers: shared hit=2
Planning Time: 0.168 ms
Execution Time: 1.515 ms
```

Wall time including plan output: 2ms

## portfolio page (first 100 matching a search)

```
Limit  (cost=3441.40..3453.45 rows=101 width=328) (actual time=28.445..28.880 rows=100.00 loops=1)
  Buffers: shared hit=1000
  ->  GroupAggregate  (cost=3441.40..3465.25 rows=200 width=328) (actual time=28.444..28.877 rows=100.00 loops=1)
        Group Key: occurrences.key
        Buffers: shared hit=1000
        ->  Sort  (cost=3441.40..3443.01 rows=645 width=326) (actual time=28.425..28.461 rows=1383.00 loops=1)
              Sort Key: occurrences.key COLLATE "C", occurrences.status
              Sort Method: quicksort  Memory: 206kB
              Buffers: shared hit=1000
              ->  Subquery Scan on occurrences  (cost=1.00..3411.30 rows=645 width=326) (actual time=0.061..28.161 rows=1383.00 loops=1)
                    Buffers: shared hit=1000
                    ->  Nested Loop Left Join  (cost=1.00..3404.85 rows=645 width=326) (actual time=0.060..28.100 rows=1383.00 loops=1)
                          Join Filter: (a.component_id = c.id)
                          Buffers: shared hit=1000
                          ->  Seq Scan on supply_chain_components c  (cost=0.50..3385.00 rows=645 width=124) (actual time=0.059..27.836 rows=1383.00 loops=1)
                                Filter: ((snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])) AND ((lower(COALESCE(purl_name, name)) ~~ '%pkg-12%'::text) OR (lower(COALESCE(purl, ''::text)) ~~ '%pkg-12%'::text) OR (lower(name) ~~ '%pkg-12%'::text)) AND (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > ''::text))
                                Rows Removed by Filter: 48817
                                Buffers: shared hit=1000
                          ->  Materialize  (cost=0.50..0.51 rows=1 width=122) (actual time=0.000..0.000 rows=0.00 loops=1383)
                                Storage: Memory  Maximum Storage: 17kB
                                ->  Seq Scan on supply_chain_component_assessments a  (cost=0.50..0.50 rows=1 width=122) (actual time=0.000..0.000 rows=0.00 loops=1)
                                      Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
Planning:
  Buffers: shared hit=9
Planning Time: 0.165 ms
Execution Time: 28.898 ms
```

Wall time including plan output: 30ms

## dependency graph (150 most shared, at least 2 repositories)

```
Nested Loop  (cost=5136.14..6200.29 rows=150 width=272) (actual time=35.041..35.835 rows=150.00 loops=1)
  Buffers: shared hit=1450, temp read=367 written=368
  ->  Nested Loop  (cost=5135.72..6135.43 rows=150 width=204) (actual time=34.984..35.124 rows=150.00 loops=1)
        Buffers: shared hit=1450, temp read=367 written=368
        ->  Limit  (cost=5135.43..5135.81 rows=150 width=144) (actual time=34.973..34.982 rows=150.00 loops=1)
              Buffers: shared hit=1000, temp read=367 written=368
              ->  Sort  (cost=5135.43..5139.62 rows=1673 width=144) (actual time=34.972..34.975 rows=150.00 loops=1)
                    Sort Key: (count(DISTINCT c_1.snapshot_id)) DESC, (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text) COLLATE "C"
                    Sort Method: top-N heapsort  Memory: 153kB
                    Buffers: shared hit=1000, temp read=367 written=368
                    ->  WindowAgg  (cost=5066.23..5066.60 rows=1673 width=144) (actual time=34.314..34.584 rows=4378.00 loops=1)
                          Window: w1 AS ()
                          Storage: Memory  Maximum Storage: 1417kB
                          Buffers: shared hit=1000, temp read=367 written=368
                          ->  GroupAggregate  (cost=4447.69..5045.69 rows=1673 width=136) (actual time=21.975..33.687 rows=4378.00 loops=1)
                                Group Key: (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text)
                                Filter: (count(DISTINCT c_1.snapshot_id) >= '2'::bigint)
                                Rows Removed by Filter: 182
                                Buffers: shared hit=1000, temp read=367 written=368
                                ->  Sort  (cost=4447.69..4527.14 rows=31779 width=60) (actual time=21.918..24.175 rows=50200.00 loops=1)
                                      Sort Key: (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text) COLLATE "C", c_1.snapshot_id
                                      Sort Method: external merge  Disk: 2936kB
                                      Buffers: shared hit=1000, temp read=367 written=368
                                      ->  Seq Scan on supply_chain_components c_1  (cost=0.50..2071.29 rows=31779 width=60) (actual time=0.008..6.519 rows=50200.00 loops=1)
                                            Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
                                            Buffers: shared hit=1000
        ->  Index Scan using supply_chain_components_pkey on supply_chain_components c  (cost=0.29..6.65 rows=1 width=68) (actual time=0.001..0.001 rows=1.00 loops=150)
              Index Cond: (id = ((array_agg(c_1.id)))[1])
              Index Searches: 150
              Buffers: shared hit=450
  ->  Aggregate  (cost=0.41..0.42 rows=1 width=64) (actual time=0.005..0.005 rows=1.00 loops=150)
        ->  Sort  (cost=0.31..0.33 rows=10 width=114) (actual time=0.003..0.004 rows=17.23 loops=150)
              Sort Key: ((COALESCE(a.status, 'unassessed'::character varying))::text)
              Sort Method: quicksort  Memory: 25kB
              ->  Hash Left Join  (cost=0.02..0.14 rows=10 width=114) (actual time=0.001..0.002 rows=17.23 loops=150)
                    Hash Cond: (occurrence.id = a.component_id)
                    ->  Function Scan on unnest occurrence  (cost=0.00..0.10 rows=10 width=8) (actual time=0.001..0.001 rows=17.23 loops=150)
                    ->  Hash  (cost=0.00..0.00 rows=1 width=122) (actual time=0.004..0.005 rows=0.00 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 8kB
                          ->  Seq Scan on supply_chain_component_assessments a  (cost=0.00..0.00 rows=1 width=122) (actual time=0.004..0.004 rows=0.00 loops=1)
Planning:
  Buffers: shared hit=47
Planning Time: 0.346 ms
Execution Time: 36.032 ms
```

Wall time including plan output: 38ms

## coordinate occurrences

```
Limit  (cost=46.10..54.51 rows=1 width=19) (actual time=0.017..0.018 rows=0.00 loops=1)
  Buffers: shared hit=5
  ->  Nested Loop  (cost=46.10..54.51 rows=1 width=19) (actual time=0.017..0.017 rows=0.00 loops=1)
        Buffers: shared hit=5
        ->  Hash Join  (cost=45.96..53.95 rows=1 width=19) (actual time=0.017..0.017 rows=0.00 loops=1)
              Hash Cond: (st.latest_snapshot_id = c.snapshot_id)
              Buffers: shared hit=5
              ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=16) (actual time=0.009..0.009 rows=1.00 loops=1)
                    Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                    Buffers: shared hit=2
              ->  Hash  (cost=45.45..45.45 rows=1 width=19) (actual time=0.006..0.006 rows=0.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 8kB
                    Buffers: shared hit=3
                    ->  Bitmap Heap Scan on supply_chain_components c  (cost=4.50..45.45 rows=1 width=19) (actual time=0.006..0.006 rows=0.00 loops=1)
                          Recheck Cond: (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text = 'npm\x01\x01pkg-0005\x015.0.2'::text)
                          Filter: (((COALESCE(ecosystem, ''::character varying))::text = 'npm'::text) AND (COALESCE(purl_namespace, ''::text) = ''::text) AND (COALESCE(purl_name, name) = 'pkg-0005'::text) AND (COALESCE(purl_version, version, ''::text) = '5.0.2'::text))
                          Buffers: shared hit=3
                          ->  Bitmap Index Scan on supply_chain_components_coordinate_key  (cost=0.00..4.50 rows=11 width=0) (actual time=0.005..0.005 rows=0.00 loops=1)
                                Index Cond: (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text = 'npm\x01\x01pkg-0005\x015.0.2'::text)
                                Index Searches: 1
                                Buffers: shared hit=3
        ->  Index Scan using repositories_pkey on repositories r  (cost=0.14..0.53 rows=1 width=16) (never executed)
              Index Cond: (id = st.repository_id)
              Index Searches: 0
Planning:
  Buffers: shared hit=14
Planning Time: 0.228 ms
Execution Time: 0.031 ms
```

Wall time including plan output: 1ms

## review queue (no results yet)

```
Limit  (cost=0.92..312.27 rows=101 width=8) (actual time=0.018..0.925 rows=101.00 loops=1)
  Buffers: shared hit=12
  ->  Nested Loop  (cost=0.92..97778.08 rows=31718 width=8) (actual time=0.018..0.921 rows=101.00 loops=1)
        Join Filter: (c.snapshot_id = st.latest_snapshot_id)
        Rows Removed by Join Filter: 19900
        Buffers: shared hit=12
        ->  Merge Left Join  (cost=0.41..2616.92 rows=49949 width=16) (actual time=0.011..0.025 rows=101.00 loops=1)
              Merge Cond: (c.id = pr.component_id)
              Filter: ((COALESCE(pr.verdict, 'unknown'::character varying))::text <> 'approved'::text)
              Buffers: shared hit=8
              ->  Index Scan using supply_chain_components_pkey on supply_chain_components c  (cost=0.29..2441.79 rows=50200 width=16) (actual time=0.009..0.015 rows=101.00 loops=1)
                    Index Cond: (id > 0)
                    Index Searches: 1
                    Buffers: shared hit=7
              ->  Index Scan using supply_chain_policy_results_current on supply_chain_policy_results pr  (cost=0.12..46.33 rows=220 width=58) (actual time=0.000..0.000 rows=0.00 loops=1)
                    Index Searches: 1
                    Buffers: shared hit=1
        ->  Materialize  (cost=0.50..8.63 rows=127 width=8) (actual time=0.000..0.004 rows=198.03 loops=101)
              Storage: Memory  Maximum Storage: 23kB
              Buffers: shared hit=4
              ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=8) (actual time=0.005..0.021 rows=200.00 loops=1)
                    Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                    Buffers: shared hit=4
Planning:
  Buffers: shared hit=103 read=3
Planning Time: 0.222 ms
Execution Time: 0.934 ms
```

Wall time including plan output: 2ms


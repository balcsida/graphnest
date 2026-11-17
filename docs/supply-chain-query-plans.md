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

- A component page reads its keys from that index in key order, stops when
  the page is full, and aggregates only the occurrences of those keys. Its cost
  follows the page, not the inventory. On a table this small the planner still
  joins the keys to their occurrences with one sequential pass, because that is
  cheaper than a hundred index probes; on a large table it probes the index.
- A page filtered by search, license or assessment has to find the matches
  first, so it reads the occurrences in scope once and sorts the matching keys.
  It no longer aggregates all of them.
- The overview counts coordinates in one pass (hash aggregate by key, folded
  per ecosystem) and reads assessment counts from the `(snapshot_id, status)`
  index alone. It used to sort every occurrence by four text columns in the
  database collation, which was most of its time.
- The dependency graph is one pass that ranks every package, then reads
  assessments for the occurrences of the returned packages only.

## A large inventory

The same statements on a synthetic inventory of the size at which v0.13.0
timed out: 3,000 repositories, 600,000 occurrences in the latest snapshots,
about 59,000 unique coordinates, and four retained earlier snapshots per
repository (3,000,000 component rows). PostgreSQL 18.6 limited to one CPU,
default settings (`work_mem` 4 MB), warm cache, whole-inventory scope:

| Request | v0.13.0 | Now |
| --- | --- | --- |
| Component page, no filter or ecosystem only | 2.7 s | 15 ms |
| Component page filtered by assessment, license or search | 2.7 s or more | 0.6 to 1.2 s |
| Overview | 3.9 s in four statements | 0.75 s |
| Facets | 2.4 s in three statements | 0.5 s |
| Dependency graph | up to 30 component pages, 80 s | 0.8 s in one request |

A scope of 300 of those repositories answers every request in under 0.2 s,
and a scope of five in a few milliseconds.

## The recorded dataset

Dataset: 200 repositories x 250 components = 50000 occurrences, ~5000 unique coordinates; seeded in 2.106s.
PostgreSQL: PostgreSQL 18.6 (Debian 18.6-1.pgdg13+2) on aarch64-unknown-linux-gnu, compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit

## overview counts

```
Aggregate  (cost=31.57..31.58 rows=1 width=24) (actual time=0.092..0.092 rows=1.00 loops=1)
  Buffers: shared hit=17
  ->  Hash Left Join  (cost=26.50..29.57 rows=200 width=20) (actual time=0.056..0.081 rows=200.00 loops=1)
        Hash Cond: (st.latest_snapshot_id = snap.id)
        Buffers: shared hit=17
        ->  Hash Left Join  (cost=9.00..11.54 rows=200 width=8) (actual time=0.029..0.044 rows=200.00 loops=1)
              Hash Cond: (r.id = st.repository_id)
              Buffers: shared hit=4
              ->  Function Scan on unnest r  (cost=0.00..2.00 rows=200 width=8) (actual time=0.007..0.011 rows=200.00 loops=1)
              ->  Hash  (cost=6.50..6.50 rows=200 width=16) (actual time=0.021..0.021 rows=200.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 18kB
                    Buffers: shared hit=4
                    ->  Seq Scan on supply_chain_streams st  (cost=0.00..6.50 rows=200 width=16) (actual time=0.003..0.013 rows=200.00 loops=1)
                          Filter: ((stream_key)::text = 'github:source'::text)
                          Buffers: shared hit=4
        ->  Hash  (cost=15.00..15.00 rows=200 width=20) (actual time=0.026..0.026 rows=200.00 loops=1)
              Buckets: 1024  Batches: 1  Memory Usage: 19kB
              Buffers: shared hit=13
              ->  Seq Scan on supply_chain_snapshots snap  (cost=0.00..15.00 rows=200 width=20) (actual time=0.001..0.017 rows=200.00 loops=1)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=17
Planning Time: 0.098 ms
Execution Time: 0.105 ms
```

Wall time including plan output: 1ms

## coordinates per ecosystem (overview)

```
HashAggregate  (cost=2919.10..2922.60 rows=200 width=136) (actual time=13.529..13.530 rows=6.00 loops=1)
  Group Key: min((COALESCE(c.ecosystem, ''::character varying))::text)
  Batches: 1  Memory Usage: 32kB
  Buffers: shared hit=1000
  ->  HashAggregate  (cost=2706.87..2818.57 rows=4468 width=88) (actual time=12.850..13.146 rows=4560.00 loops=1)
        Group Key: ((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text
        Batches: 1  Memory Usage: 785kB
        Buffers: shared hit=1000
        ->  Seq Scan on supply_chain_components c  (cost=0.50..2230.18 rows=31779 width=73) (actual time=0.008..7.578 rows=50200.00 loops=1)
              Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
              Buffers: shared hit=1000
Planning:
  Buffers: shared hit=83
Planning Time: 0.191 ms
Execution Time: 13.547 ms
```

Wall time including plan output: 14ms

## assessment counts (overview and facets)

```
HashAggregate  (cost=0.50..0.51 rows=1 width=90) (actual time=0.002..0.002 rows=0.00 loops=1)
  Group Key: status
  Batches: 1  Memory Usage: 32kB
  ->  Seq Scan on supply_chain_component_assessments  (cost=0.50..0.50 rows=1 width=82) (actual time=0.002..0.002 rows=0.00 loops=1)
        Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
Planning:
  Buffers: shared hit=12 read=2
Planning Time: 0.093 ms
Execution Time: 0.010 ms
```

Wall time including plan output: 1ms

## portfolio page (first 100)

```
GroupAggregate  (cost=1939.55..1975.88 rows=101 width=328) (actual time=7.919..8.080 rows=101.00 loops=1)
  Group Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text)
  Buffers: shared hit=1531
  ->  Sort  (cost=1939.55..1942.39 rows=1135 width=262) (actual time=7.827..7.840 rows=527.00 loops=1)
        Sort Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text) COLLATE "C", a.status
        Sort Method: quicksort  Memory: 80kB
        Buffers: shared hit=1531
        ->  Hash Left Join  (cost=164.80..1881.96 rows=1135 width=262) (actual time=0.311..7.740 rows=527.00 loops=1)
              Hash Cond: (c.id = a.component_id)
              Buffers: shared hit=1531
              ->  Hash Join  (cost=164.78..1878.97 rows=1135 width=156) (actual time=0.309..7.711 rows=527.00 loops=1)
                    Hash Cond: (c.snapshot_id = scope.snapshot_id)
                    Buffers: shared hit=1531
                    ->  Hash Join  (cost=160.28..1831.90 rows=1135 width=156) (actual time=0.292..7.651 rows=527.00 loops=1)
                          Hash Cond: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text = (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text))
                          Buffers: shared hit=1531
                          ->  Seq Scan on supply_chain_components c  (cost=0.00..1502.00 rows=50200 width=124) (actual time=0.003..1.601 rows=50200.00 loops=1)
                                Buffers: shared hit=1000
                          ->  Hash  (cost=159.02..159.02 rows=101 width=32) (actual time=0.287..0.287 rows=101.00 loops=1)
                                Buckets: 1024  Batches: 1  Memory Usage: 14kB
                                Buffers: shared hit=531
                                ->  Limit  (cost=0.41..159.02 rows=101 width=32) (actual time=0.019..0.280 rows=101.00 loops=1)
                                      Buffers: shared hit=531
                                      ->  Group  (cost=0.41..7016.70 rows=4468 width=32) (actual time=0.018..0.277 rows=101.00 loops=1)
                                            Group Key: ((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text
                                            Buffers: shared hit=531
                                            ->  Index Scan using supply_chain_components_coordinate_key on supply_chain_components c_1  (cost=0.41..6870.23 rows=31779 width=32) (actual time=0.018..0.260 rows=522.00 loops=1)
                                                  Index Cond: ((((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > ''::text) AND (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                                                  Index Searches: 1
                                                  Buffers: shared hit=531
                    ->  Hash  (cost=2.00..2.00 rows=200 width=8) (actual time=0.015..0.015 rows=200.00 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 16kB
                          ->  Function Scan on unnest scope  (cost=0.00..2.00 rows=200 width=8) (actual time=0.006..0.010 rows=200.00 loops=1)
              ->  Hash  (cost=0.00..0.00 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 8kB
                    ->  Seq Scan on supply_chain_component_assessments a  (cost=0.00..0.00 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
Planning:
  Buffers: shared hit=33
Planning Time: 0.266 ms
Execution Time: 8.104 ms
```

Wall time including plan output: 9ms

## portfolio page (100 from the middle)

```
GroupAggregate  (cost=1906.97..1943.29 rows=101 width=328) (actual time=8.300..8.762 rows=101.00 loops=1)
  Group Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text)
  Buffers: shared hit=2551
  ->  Sort  (cost=1906.97..1909.81 rows=1135 width=262) (actual time=8.286..8.319 rows=1554.00 loops=1)
        Sort Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text) COLLATE "C", a.status
        Sort Method: quicksort  Memory: 243kB
        Buffers: shared hit=2551
        ->  Hash Left Join  (cost=132.21..1849.38 rows=1135 width=262) (actual time=0.621..8.009 rows=1554.00 loops=1)
              Hash Cond: (c.id = a.component_id)
              Buffers: shared hit=2551
              ->  Hash Join  (cost=132.20..1846.39 rows=1135 width=156) (actual time=0.618..7.925 rows=1554.00 loops=1)
                    Hash Cond: (c.snapshot_id = scope.snapshot_id)
                    Buffers: shared hit=2551
                    ->  Hash Join  (cost=127.70..1799.32 rows=1135 width=156) (actual time=0.603..7.794 rows=1554.00 loops=1)
                          Hash Cond: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text = (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text))
                          Buffers: shared hit=2551
                          ->  Seq Scan on supply_chain_components c  (cost=0.00..1502.00 rows=50200 width=124) (actual time=0.002..1.481 rows=50200.00 loops=1)
                                Buffers: shared hit=1000
                          ->  Hash  (cost=126.44..126.44 rows=101 width=32) (actual time=0.570..0.571 rows=101.00 loops=1)
                                Buckets: 1024  Batches: 1  Memory Usage: 14kB
                                Buffers: shared hit=1551
                                ->  Limit  (cost=0.41..126.44 rows=101 width=32) (actual time=0.025..0.565 rows=101.00 loops=1)
                                      Buffers: shared hit=1551
                                      ->  Group  (cost=0.41..5483.01 rows=4394 width=32) (actual time=0.025..0.561 rows=101.00 loops=1)
                                            Group Key: ((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text
                                            Buffers: shared hit=1551
                                            ->  Index Scan using supply_chain_components_coordinate_key on supply_chain_components c_1  (cost=0.41..5378.71 rows=15354 width=32) (actual time=0.025..0.524 rows=1539.00 loops=1)
                                                  Index Cond: ((((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > 'npm\x01\x01pkg-2500\x017.0.1'::text) AND (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                                                  Index Searches: 1
                                                  Buffers: shared hit=1551
                    ->  Hash  (cost=2.00..2.00 rows=200 width=8) (actual time=0.015..0.015 rows=200.00 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 16kB
                          ->  Function Scan on unnest scope  (cost=0.00..2.00 rows=200 width=8) (actual time=0.006..0.011 rows=200.00 loops=1)
              ->  Hash  (cost=0.00..0.00 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 8kB
                    ->  Seq Scan on supply_chain_component_assessments a  (cost=0.00..0.00 rows=1 width=122) (actual time=0.001..0.001 rows=0.00 loops=1)
Planning:
  Buffers: shared hit=4
Planning Time: 0.236 ms
Execution Time: 8.787 ms
```

Wall time including plan output: 10ms

## portfolio page (first 100 matching a search)

```
GroupAggregate  (cost=5687.71..5690.08 rows=45 width=328) (actual time=52.757..53.202 rows=100.00 loops=1)
  Group Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text)
  Buffers: shared hit=2000
  ->  Sort  (cost=5687.71..5687.83 rows=45 width=262) (actual time=52.732..52.767 rows=1383.00 loops=1)
        Sort Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text) COLLATE "C", a.status
        Sort Method: quicksort  Memory: 232kB
        Buffers: shared hit=2000
        ->  Hash Left Join  (cost=3422.90..5686.48 rows=45 width=262) (actual time=26.676..52.473 rows=1383.00 loops=1)
              Hash Cond: (c.id = a.component_id)
              Buffers: shared hit=2000
              ->  Hash Join  (cost=3422.89..5686.35 rows=45 width=156) (actual time=26.667..52.393 rows=1383.00 loops=1)
                    Hash Cond: (c.snapshot_id = scope.snapshot_id)
                    Buffers: shared hit=2000
                    ->  Hash Join  (cost=3418.38..5680.16 rows=45 width=156) (actual time=26.651..52.259 rows=1383.00 loops=1)
                          Hash Cond: (((((((((COALESCE(c.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c.purl_name, c.name)) || '\x01'::text) || COALESCE(c.purl_version, c.version, ''::text)))::text = (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text))
                          Buffers: shared hit=2000
                          ->  Seq Scan on supply_chain_components c  (cost=0.00..2255.00 rows=2007 width=124) (actual time=0.050..25.308 rows=1383.00 loops=1)
                                Filter: ((lower(COALESCE(purl_name, name)) ~~ '%pkg-12%'::text) OR (lower(COALESCE(purl, ''::text)) ~~ '%pkg-12%'::text) OR (lower(name) ~~ '%pkg-12%'::text))
                                Rows Removed by Filter: 48817
                                Buffers: shared hit=1000
                          ->  Hash  (cost=3417.12..3417.12 rows=101 width=32) (actual time=26.598..26.599 rows=100.00 loops=1)
                                Buckets: 1024  Batches: 1  Memory Usage: 14kB
                                Buffers: shared hit=1000
                                ->  Limit  (cost=3416.87..3417.12 rows=101 width=32) (actual time=26.585..26.591 rows=100.00 loops=1)
                                      Buffers: shared hit=1000
                                      ->  Sort  (cost=3416.87..3417.37 rows=200 width=32) (actual time=26.585..26.587 rows=100.00 loops=1)
                                            Sort Key: (((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text) COLLATE "C"
                                            Sort Method: quicksort  Memory: 25kB
                                            Buffers: shared hit=1000
                                            ->  HashAggregate  (cost=3407.23..3409.23 rows=200 width=32) (actual time=26.567..26.571 rows=100.00 loops=1)
                                                  Group Key: ((((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)) || '\x01'::text) || COALESCE(c_1.purl_version, c_1.version, ''::text)))::text
                                                  Batches: 1  Memory Usage: 32kB
                                                  Buffers: shared hit=1000
                                                  ->  Seq Scan on supply_chain_components c_1  (cost=0.50..3404.05 rows=1270 width=32) (actual time=0.053..26.444 rows=1383.00 loops=1)
                                                        Filter: ((snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])) AND ((lower(COALESCE(purl_name, name)) ~~ '%pkg-12%'::text) OR (lower(COALESCE(purl, ''::text)) ~~ '%pkg-12%'::text) OR (lower(name) ~~ '%pkg-12%'::text)) AND (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text > ''::text))
                                                        Rows Removed by Filter: 48817
                                                        Buffers: shared hit=1000
                    ->  Hash  (cost=2.00..2.00 rows=200 width=8) (actual time=0.015..0.015 rows=200.00 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 16kB
                          ->  Function Scan on unnest scope  (cost=0.00..2.00 rows=200 width=8) (actual time=0.006..0.010 rows=200.00 loops=1)
              ->  Hash  (cost=0.00..0.00 rows=1 width=122) (actual time=0.007..0.007 rows=0.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 8kB
                    ->  Seq Scan on supply_chain_component_assessments a  (cost=0.00..0.00 rows=1 width=122) (actual time=0.007..0.007 rows=0.00 loops=1)
Planning:
  Buffers: shared hit=11
Planning Time: 0.260 ms
Execution Time: 53.229 ms
```

Wall time including plan output: 54ms

## dependency graph (150 most shared, at least 2 repositories)

```
Nested Loop  (cost=5136.14..6200.29 rows=150 width=272) (actual time=33.958..34.697 rows=150.00 loops=1)
  Buffers: shared hit=1450, temp read=367 written=368
  ->  Nested Loop  (cost=5135.72..6135.43 rows=150 width=204) (actual time=33.908..34.039 rows=150.00 loops=1)
        Buffers: shared hit=1450, temp read=367 written=368
        ->  Limit  (cost=5135.43..5135.81 rows=150 width=144) (actual time=33.898..33.906 rows=150.00 loops=1)
              Buffers: shared hit=1000, temp read=367 written=368
              ->  Sort  (cost=5135.43..5139.62 rows=1673 width=144) (actual time=33.898..33.901 rows=150.00 loops=1)
                    Sort Key: (count(DISTINCT c_1.snapshot_id)) DESC, (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text) COLLATE "C"
                    Sort Method: top-N heapsort  Memory: 153kB
                    Buffers: shared hit=1000, temp read=367 written=368
                    ->  WindowAgg  (cost=5066.23..5066.60 rows=1673 width=144) (actual time=33.278..33.528 rows=4378.00 loops=1)
                          Window: w1 AS ()
                          Storage: Memory  Maximum Storage: 1417kB
                          Buffers: shared hit=1000, temp read=367 written=368
                          ->  GroupAggregate  (cost=4447.69..5045.69 rows=1673 width=136) (actual time=22.318..32.617 rows=4378.00 loops=1)
                                Group Key: (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text)
                                Filter: (count(DISTINCT c_1.snapshot_id) >= '2'::bigint)
                                Rows Removed by Filter: 182
                                Buffers: shared hit=1000, temp read=367 written=368
                                ->  Sort  (cost=4447.69..4527.14 rows=31779 width=60) (actual time=22.277..24.409 rows=50200.00 loops=1)
                                      Sort Key: (((((((COALESCE(c_1.ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(c_1.purl_namespace, ''::text)) || '\x01'::text) || COALESCE(c_1.purl_name, c_1.name)))::text) COLLATE "C", c_1.snapshot_id
                                      Sort Method: external merge  Disk: 2936kB
                                      Buffers: shared hit=1000, temp read=367 written=368
                                      ->  Seq Scan on supply_chain_components c_1  (cost=0.50..2071.29 rows=31779 width=60) (actual time=0.007..6.293 rows=50200.00 loops=1)
                                            Filter: (snapshot_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[]))
                                            Buffers: shared hit=1000
        ->  Index Scan using supply_chain_components_pkey on supply_chain_components c  (cost=0.29..6.65 rows=1 width=68) (actual time=0.001..0.001 rows=1.00 loops=150)
              Index Cond: (id = ((array_agg(c_1.id)))[1])
              Index Searches: 150
              Buffers: shared hit=450
  ->  Aggregate  (cost=0.41..0.42 rows=1 width=64) (actual time=0.004..0.004 rows=1.00 loops=150)
        ->  Sort  (cost=0.31..0.33 rows=10 width=114) (actual time=0.003..0.003 rows=17.23 loops=150)
              Sort Key: ((COALESCE(a.status, 'unassessed'::character varying))::text)
              Sort Method: quicksort  Memory: 25kB
              ->  Hash Left Join  (cost=0.02..0.14 rows=10 width=114) (actual time=0.001..0.002 rows=17.23 loops=150)
                    Hash Cond: (occurrence.id = a.component_id)
                    ->  Function Scan on unnest occurrence  (cost=0.00..0.10 rows=10 width=8) (actual time=0.001..0.001 rows=17.23 loops=150)
                    ->  Hash  (cost=0.00..0.00 rows=1 width=122) (actual time=0.004..0.004 rows=0.00 loops=1)
                          Buckets: 1024  Batches: 1  Memory Usage: 8kB
                          ->  Seq Scan on supply_chain_component_assessments a  (cost=0.00..0.00 rows=1 width=122) (actual time=0.004..0.004 rows=0.00 loops=1)
Planning:
  Buffers: shared hit=47
Planning Time: 0.286 ms
Execution Time: 34.866 ms
```

Wall time including plan output: 36ms

## coordinate occurrences

```
Limit  (cost=46.10..54.51 rows=1 width=19) (actual time=0.020..0.020 rows=0.00 loops=1)
  Buffers: shared hit=5
  ->  Nested Loop  (cost=46.10..54.51 rows=1 width=19) (actual time=0.020..0.020 rows=0.00 loops=1)
        Buffers: shared hit=5
        ->  Hash Join  (cost=45.96..53.95 rows=1 width=19) (actual time=0.020..0.020 rows=0.00 loops=1)
              Hash Cond: (st.latest_snapshot_id = c.snapshot_id)
              Buffers: shared hit=5
              ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=16) (actual time=0.006..0.006 rows=1.00 loops=1)
                    Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                    Buffers: shared hit=2
              ->  Hash  (cost=45.45..45.45 rows=1 width=19) (actual time=0.012..0.013 rows=0.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 8kB
                    Buffers: shared hit=3
                    ->  Bitmap Heap Scan on supply_chain_components c  (cost=4.50..45.45 rows=1 width=19) (actual time=0.012..0.012 rows=0.00 loops=1)
                          Recheck Cond: (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text = 'npm\x01\x01pkg-0005\x015.0.2'::text)
                          Filter: (((COALESCE(ecosystem, ''::character varying))::text = 'npm'::text) AND (COALESCE(purl_namespace, ''::text) = ''::text) AND (COALESCE(purl_name, name) = 'pkg-0005'::text) AND (COALESCE(purl_version, version, ''::text) = '5.0.2'::text))
                          Buffers: shared hit=3
                          ->  Bitmap Index Scan on supply_chain_components_coordinate_key  (cost=0.00..4.50 rows=11 width=0) (actual time=0.010..0.010 rows=0.00 loops=1)
                                Index Cond: (((((((((COALESCE(ecosystem, ''::character varying))::text || '\x01'::text) || COALESCE(purl_namespace, ''::text)) || '\x01'::text) || COALESCE(purl_name, name)) || '\x01'::text) || COALESCE(purl_version, version, ''::text)))::text = 'npm\x01\x01pkg-0005\x015.0.2'::text)
                                Index Searches: 1
                                Buffers: shared hit=3
        ->  Index Scan using repositories_pkey on repositories r  (cost=0.14..0.53 rows=1 width=16) (never executed)
              Index Cond: (id = st.repository_id)
              Index Searches: 0
Planning:
  Buffers: shared hit=14
Planning Time: 0.181 ms
Execution Time: 0.032 ms
```

Wall time including plan output: 1ms

## review queue (no results yet)

```
Limit  (cost=0.92..312.27 rows=101 width=8) (actual time=0.018..0.824 rows=101.00 loops=1)
  Buffers: shared hit=12
  ->  Nested Loop  (cost=0.92..97778.08 rows=31718 width=8) (actual time=0.018..0.821 rows=101.00 loops=1)
        Join Filter: (c.snapshot_id = st.latest_snapshot_id)
        Rows Removed by Join Filter: 19900
        Buffers: shared hit=12
        ->  Merge Left Join  (cost=0.41..2616.92 rows=49949 width=16) (actual time=0.011..0.024 rows=101.00 loops=1)
              Merge Cond: (c.id = pr.component_id)
              Filter: ((COALESCE(pr.verdict, 'unknown'::character varying))::text <> 'approved'::text)
              Buffers: shared hit=8
              ->  Index Scan using supply_chain_components_pkey on supply_chain_components c  (cost=0.29..2441.79 rows=50200 width=16) (actual time=0.009..0.014 rows=101.00 loops=1)
                    Index Cond: (id > 0)
                    Index Searches: 1
                    Buffers: shared hit=7
              ->  Index Scan using supply_chain_policy_results_current on supply_chain_policy_results pr  (cost=0.12..46.33 rows=220 width=58) (actual time=0.000..0.000 rows=0.00 loops=1)
                    Index Searches: 1
                    Buffers: shared hit=1
        ->  Materialize  (cost=0.50..8.63 rows=127 width=8) (actual time=0.000..0.004 rows=198.03 loops=101)
              Storage: Memory  Maximum Storage: 23kB
              Buffers: shared hit=4
              ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=8) (actual time=0.006..0.020 rows=200.00 loops=1)
                    Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                    Buffers: shared hit=4
Planning:
  Buffers: shared hit=91 read=3
Planning Time: 0.264 ms
Execution Time: 0.834 ms
```

Wall time including plan output: 2ms


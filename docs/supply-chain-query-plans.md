# Supply-chain portfolio query plans

Recorded by `TestSupplyChainPortfolioQueryPlans` (`go test -tags=integration
./internal/postgres -run TestSupplyChainPortfolioQueryPlans` with
`GRAPHNEST_TEST_SUPPLY_CHAIN_PLANS=<file>`) on 2026-09-22 against PostgreSQL
18.6 in Docker (OrbStack, Apple Silicon). Times are measurements of this
dataset in this environment, not a latency commitment.

Reading: overview counts, coordinate lookup, and the review queue use the
stream/component indexes and stay in low milliseconds. Unique-coordinate
counting and the portfolio page aggregate every authorized latest-snapshot
occurrence (a full pass with an external-merge sort at 50k rows, ~110-120 ms
here). The upgrade path when that grows past the budget is a per-snapshot
coordinate summary table refreshed at publication (decision D7 in the
execution plan); it is not needed at this scale.


Dataset: 200 repositories x 250 components = 50000 occurrences, ~5000 unique coordinates; seeded in 2.513s.
PostgreSQL: PostgreSQL 18.6 (Debian 18.6-1.pgdg13+2) on aarch64-unknown-linux-gnu, compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit

## overview counts

```
Aggregate  (cost=31.57..31.58 rows=1 width=24) (actual time=0.108..0.108 rows=1.00 loops=1)
  Buffers: shared hit=17
  ->  Hash Left Join  (cost=26.50..29.57 rows=200 width=20) (actual time=0.072..0.101 rows=200.00 loops=1)
        Hash Cond: (st.latest_snapshot_id = snap.id)
        Buffers: shared hit=17
        ->  Hash Left Join  (cost=9.00..11.54 rows=200 width=8) (actual time=0.037..0.054 rows=200.00 loops=1)
              Hash Cond: (r.id = st.repository_id)
              Buffers: shared hit=4
              ->  Function Scan on unnest r  (cost=0.00..2.00 rows=200 width=8) (actual time=0.008..0.013 rows=200.00 loops=1)
              ->  Hash  (cost=6.50..6.50 rows=200 width=16) (actual time=0.028..0.028 rows=200.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 18kB
                    Buffers: shared hit=4
                    ->  Seq Scan on supply_chain_streams st  (cost=0.00..6.50 rows=200 width=16) (actual time=0.005..0.018 rows=200.00 loops=1)
                          Filter: ((stream_key)::text = 'github:source'::text)
                          Buffers: shared hit=4
        ->  Hash  (cost=15.00..15.00 rows=200 width=20) (actual time=0.034..0.034 rows=200.00 loops=1)
              Buckets: 1024  Batches: 1  Memory Usage: 19kB
              Buffers: shared hit=13
              ->  Seq Scan on supply_chain_snapshots snap  (cost=0.00..15.00 rows=200 width=20) (actual time=0.002..0.023 rows=200.00 loops=1)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=107 read=1
Planning Time: 0.207 ms
Execution Time: 0.127 ms
```

Wall time including plan output: 1ms

## unique coordinates

```
Aggregate  (cost=4562.43..4562.44 rows=1 width=8) (actual time=112.801..112.803 rows=1.00 loops=1)
  Buffers: shared hit=1009, temp read=576 written=577
  ->  Sort  (cost=4403.04..4482.74 rows=31877 width=72) (actual time=97.434..106.055 rows=50200.00 loops=1)
        Sort Key: (ROW(c.ecosystem, COALESCE(c.purl_namespace, ''::text), COALESCE(c.purl_name, c.name), COALESCE(c.purl_version, c.version, ''::text)))
        Sort Method: external merge  Disk: 4608kB
        Buffers: shared hit=1009, temp read=576 written=577
        ->  Hash Join  (cost=9.59..2018.61 rows=31877 width=72) (actual time=0.035..8.901 rows=50200.00 loops=1)
              Hash Cond: (c.snapshot_id = st.latest_snapshot_id)
              Buffers: shared hit=1004
              ->  Seq Scan on supply_chain_components c  (cost=0.00..1502.00 rows=50200 width=80) (actual time=0.003..2.069 rows=50200.00 loops=1)
                    Buffers: shared hit=1000
              ->  Hash  (cost=8.00..8.00 rows=127 width=8) (actual time=0.030..0.030 rows=200.00 loops=1)
                    Buckets: 1024  Batches: 1  Memory Usage: 16kB
                    Buffers: shared hit=4
                    ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=8) (actual time=0.007..0.021 rows=200.00 loops=1)
                          Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                          Buffers: shared hit=4
Planning:
  Buffers: shared hit=93 read=1
Planning Time: 0.230 ms
Execution Time: 113.205 ms
```

Wall time including plan output: 115ms

## portfolio page (first 100)

```
Limit  (cost=3845.99..3846.24 rows=101 width=48) (actual time=119.237..119.245 rows=101.00 loops=1)
  Buffers: shared hit=1021, temp read=562 written=563
  ->  Sort  (cost=3845.99..3858.32 rows=4933 width=48) (actual time=119.236..119.241 rows=101.00 loops=1)
        Sort Key: grouped.key
        Sort Method: top-N heapsort  Memory: 32kB
        Buffers: shared hit=1021, temp read=562 written=563
        ->  Subquery Scan on grouped  (cost=3298.51..3657.10 rows=4933 width=48) (actual time=104.812..118.684 rows=4560.00 loops=1)
              Buffers: shared hit=1021, temp read=562 written=563
              ->  GroupAggregate  (cost=3298.51..3607.77 rows=4933 width=176) (actual time=104.811..118.514 rows=4560.00 loops=1)
                    Group Key: (COALESCE(c.ecosystem, ''::character varying)), (COALESCE(c.purl_namespace, ''::text)), (COALESCE(c.purl_name, c.name)), (COALESCE(c.purl_version, c.version, ''::text))
                    Buffers: shared hit=1021, temp read=562 written=563
                    ->  Sort  (cost=3298.51..3325.07 rows=10625 width=208) (actual time=104.759..112.406 rows=50200.00 loops=1)
                          Sort Key: (COALESCE(c.ecosystem, ''::character varying)), (COALESCE(c.purl_namespace, ''::text)), (COALESCE(c.purl_name, c.name)), (COALESCE(c.purl_version, c.version, ''::text)), st.repository_id
                          Sort Method: external merge  Disk: 4496kB
                          Buffers: shared hit=1021, temp read=562 written=563
                          ->  Hash Join  (cost=38.45..2587.95 rows=10625 width=208) (actual time=0.115..11.612 rows=50200.00 loops=1)
                                Hash Cond: (c.snapshot_id = snap.id)
                                Buffers: shared hit=1021
                                ->  Seq Scan on supply_chain_components c  (cost=0.00..2380.50 rows=16733 width=88) (actual time=0.006..7.557 rows=50200.00 loops=1)
                                      Filter: ((((((((COALESCE(ecosystem, ''::character varying))::text || ''::text) || COALESCE(purl_namespace, ''::text)) || ''::text) || COALESCE(purl_name, name)) || ''::text) || COALESCE(purl_version, version, ''::text)) > ''::text)
                                      Buffers: shared hit=1000
                                ->  Hash  (cost=36.86..36.86 rows=127 width=24) (actual time=0.107..0.109 rows=200.00 loops=1)
                                      Buckets: 1024  Batches: 1  Memory Usage: 19kB
                                      Buffers: shared hit=21
                                      ->  Hash Join  (cost=28.68..36.86 rows=127 width=24) (actual time=0.059..0.099 rows=200.00 loops=1)
                                            Hash Cond: (st.repository_id = r.id)
                                            Buffers: shared hit=21
                                            ->  Hash Join  (cost=18.00..25.84 rows=127 width=24) (actual time=0.037..0.065 rows=200.00 loops=1)
                                                  Hash Cond: (st.latest_snapshot_id = snap.id)
                                                  Buffers: shared hit=17
                                                  ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=16) (actual time=0.007..0.021 rows=200.00 loops=1)
                                                        Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                                                        Buffers: shared hit=4
                                                  ->  Hash  (cost=15.00..15.00 rows=200 width=8) (actual time=0.028..0.029 rows=200.00 loops=1)
                                                        Buckets: 1024  Batches: 1  Memory Usage: 16kB
                                                        Buffers: shared hit=13
                                                        ->  Seq Scan on supply_chain_snapshots snap  (cost=0.00..15.00 rows=200 width=8) (actual time=0.001..0.021 rows=200.00 loops=1)
                                                              Buffers: shared hit=13
                                            ->  Hash  (cost=10.30..10.30 rows=30 width=8) (actual time=0.020..0.020 rows=200.00 loops=1)
                                                  Buckets: 1024  Batches: 1  Memory Usage: 16kB
                                                  Buffers: shared hit=4
                                                  ->  Seq Scan on repositories r  (cost=0.00..10.30 rows=30 width=8) (actual time=0.003..0.012 rows=200.00 loops=1)
                                                        Buffers: shared hit=4
Planning:
  Buffers: shared hit=106 read=2
Planning Time: 0.472 ms
Execution Time: 119.604 ms
```

Wall time including plan output: 121ms

## coordinate occurrences

```
Limit  (cost=0.14..2037.64 rows=1 width=19) (actual time=3.533..3.534 rows=0.00 loops=1)
  Buffers: shared hit=1005
  ->  Nested Loop  (cost=0.14..2037.64 rows=1 width=19) (actual time=3.532..3.533 rows=0.00 loops=1)
        Join Filter: (st.repository_id = r.id)
        Buffers: shared hit=1005
        ->  Nested Loop  (cost=0.14..2026.97 rows=1 width=19) (actual time=3.532..3.532 rows=0.00 loops=1)
              Join Filter: (c.snapshot_id = st.latest_snapshot_id)
              Buffers: shared hit=1005
              ->  Index Scan using supply_chain_streams_pkey on supply_chain_streams st  (cost=0.14..21.06 rows=127 width=16) (actual time=0.019..0.030 rows=200.00 loops=1)
                    Index Cond: ((repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])) AND ((stream_key)::text = 'github:source'::text))
                    Index Searches: 1
                    Buffers: shared hit=5
              ->  Materialize  (cost=0.00..2004.01 rows=1 width=19) (actual time=0.017..0.017 rows=0.00 loops=200)
                    Storage: Memory  Maximum Storage: 17kB
                    Buffers: shared hit=1000
                    ->  Seq Scan on supply_chain_components c  (cost=0.00..2004.00 rows=1 width=19) (actual time=3.488..3.488 rows=0.00 loops=1)
                          Filter: (((COALESCE(ecosystem, ''::character varying))::text = 'npm'::text) AND (COALESCE(purl_namespace, ''::text) = ''::text) AND (COALESCE(purl_name, name) = 'pkg-0005'::text) AND (COALESCE(purl_version, version, ''::text) = '5.0.2'::text))
                          Rows Removed by Filter: 50200
                          Buffers: shared hit=1000
        ->  Seq Scan on repositories r  (cost=0.00..10.30 rows=30 width=16) (never executed)
Planning:
  Buffers: shared hit=11
Planning Time: 0.192 ms
Execution Time: 3.545 ms
```

Wall time including plan output: 5ms

## review queue (no results yet)

```
Limit  (cost=0.92..312.27 rows=101 width=8) (actual time=0.021..1.000 rows=101.00 loops=1)
  Buffers: shared hit=12
  ->  Nested Loop  (cost=0.92..97778.08 rows=31718 width=8) (actual time=0.021..0.996 rows=101.00 loops=1)
        Join Filter: (c.snapshot_id = st.latest_snapshot_id)
        Rows Removed by Join Filter: 19900
        Buffers: shared hit=12
        ->  Merge Left Join  (cost=0.41..2616.92 rows=49949 width=16) (actual time=0.013..0.034 rows=101.00 loops=1)
              Merge Cond: (c.id = pr.component_id)
              Filter: ((COALESCE(pr.verdict, 'unknown'::character varying))::text <> 'approved'::text)
              Buffers: shared hit=8
              ->  Index Scan using supply_chain_components_pkey on supply_chain_components c  (cost=0.29..2441.79 rows=50200 width=16) (actual time=0.011..0.022 rows=101.00 loops=1)
                    Index Cond: (id > 0)
                    Index Searches: 1
                    Buffers: shared hit=7
              ->  Index Scan using supply_chain_policy_results_current on supply_chain_policy_results pr  (cost=0.12..46.33 rows=220 width=58) (actual time=0.000..0.001 rows=0.00 loops=1)
                    Index Searches: 1
                    Buffers: shared hit=1
        ->  Materialize  (cost=0.50..8.63 rows=127 width=8) (actual time=0.000..0.004 rows=198.03 loops=101)
              Storage: Memory  Maximum Storage: 23kB
              Buffers: shared hit=4
              ->  Seq Scan on supply_chain_streams st  (cost=0.50..8.00 rows=127 width=8) (actual time=0.006..0.020 rows=200.00 loops=1)
                    Filter: (((stream_key)::text = 'github:source'::text) AND (repository_id = ANY ('{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200}'::bigint[])))
                    Buffers: shared hit=4
Planning:
  Buffers: shared hit=108 read=3
Planning Time: 0.278 ms
Execution Time: 1.013 ms
```

Wall time including plan output: 2ms


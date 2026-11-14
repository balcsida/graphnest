# Benchmarking

## One measured corpus

`make scale-bench` indexes a directory of bare Git clones through the real
indexer, PostgreSQL, and Zoekt, sends searches through the REST handler, and
writes the numbers as JSON. A fake GitHub serves each repository's head commit
as an archive.

[`benchmarks/2026-11-14.json`](benchmarks/2026-11-14.json) is one run. Its
corpus is the 40 most-starred repositories for each of Go, TypeScript, Java,
Kotlin, Rust, and Python that are not archived, not forks, and under 1 GB by
GitHub's count: 240 repositories, listed with their commits in
[`benchmarks/2026-11-14-corpus.tsv`](benchmarks/2026-11-14-corpus.tsv). It ran
in a Linux container on a 14-core Apple Silicon laptop, with PostgreSQL in a
second container and Zoekt `v0.0.0-20260717095332-3c8b39b1ef4f`.

| Measure | Result |
| --- | --- |
| Repositories indexed | 240 of 240 |
| Source | 14.0 GiB in 985,911 files |
| Cold index, one job at a time | 10 min 13 s |
| Share of that time in fetch and extraction, and in `zoekt-index` | 55% and 42% |
| Per repository | median 1.2 s, 95th percentile 10.2 s, slowest 15.6 s |
| Zoekt shards | 19.5 GiB, 1.4 times the source |
| PostgreSQL | 2.3 MiB |
| 200 searches right after indexing, 4 at a time | median 56 ms, 95th percentile 1.5 s, 6 failed after about 5 s, the search timeout |
| The same 200 searches again | median 50 ms, 95th percentile 366 ms, slowest 410 ms, none failed |

In the second pass the literal queries have medians between 26 and 59 ms. The
regular expression `foo.*bar` has a median of 359 ms. The latencies include
the failed requests.

What this run does not show:

- A GitHub download. The fetch is a local `git archive`, so the indexing time
  is a floor.
- More than one indexer job, one Zoekt process, or one host.
- SCIP indexes, graph generations, and the dependency inventory, which is
  where PostgreSQL grows. Navigation and graph queries were not measured.
- Search with more than four requests at a time, or with other queries than
  the nine in the test.

Two limits showed up on the way:

- Right after indexing, some searches take seconds and a few fail at the 5 s
  search timeout. A second pass over the same requests shows neither. The run
  did not measure why; reading the freshly written shards for the first time
  is the likely cause.
- Download and extraction of an archive share the indexer's 30 s GitHub client
  timeout. On the same laptop's macOS host, where every extracted file is
  scanned on access, the corpus took 70 minutes to index, and two repositories
  (168 MiB in 36,657 files and 241 MiB in 50,072 files) failed on that timeout.
  A slow disk or a slow link to GitHub can reach it as well.

To repeat the run, clone the repositories as `<directory>/<owner>/<name>.git`
(shallow bare clones are enough) and point the target at the directory. Both
paths have to be absolute:

```sh
make scale-bench SCALE_REPOS=/path/to/clones SCALE_REPORT=/path/to/report.json
```

Run it on Linux. On a case-insensitive file system, repositories with paths
that differ only in case fail to extract.

## Comparing Zoekt transports

Use the same query and repository selection for every comparison. The current
baseline is JSON over HTTP to Zoekt.

```sh
go test -bench . -benchmem ./...
```

Compare identical-query p95 latency and CPU at the same load and corpus. Per
ADR-0003, consider switching only if the benchmarked alternative demonstrates
at least 10% lower p95 latency or CPU than JSON HTTP on identical queries.
Until that evidence exists, the JSON adapter remains the chosen path.

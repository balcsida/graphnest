# Benchmarking

Use the same query and repository selection for every comparison. The current
baseline is JSON over HTTP to Zoekt; do not add gRPC as part of this milestone.

```sh
go test -bench . -benchmem ./...
```

When a representative benchmark exists, compare identical-query p95 latency
and CPU at the same load and corpus. Per ADR-0003, consider switching only if
the benchmarked alternative demonstrates at least 10% lower p95 latency or CPU
than JSON HTTP on identical queries. Until that evidence exists, the JSON
adapter remains the chosen path.

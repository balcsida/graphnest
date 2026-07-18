# Operations

Milestones 0-1 are a local development slice, not a production deployment.
Run Zoekt only on loopback or an isolated container network. Never expose its
port through public ingress.

The server provides `/healthz`, dependency-aware `/readyz`, and `/metrics`.
Shutdown is cooperative. Search timeouts and result limits are configurable but
always clamped server-side.

Capacity cannot be inferred from repository count. Before the OpenShift pilot,
measure source corpus size, index size, indexing duration, query concurrency,
latency, CPU, memory, and disk use.

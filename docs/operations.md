# Operations

Milestones 0-1 are local development only. Start the pinned fixture stack with:

```sh
docker compose -f deploy/compose/compose.yml up -d --wait
docker compose -f deploy/compose/compose.yml ps
```

The Compose fixture index uses repository ID `7` and the checked-in registry at
`deploy/compose/repositories.json`. PostgreSQL must become healthy for Compose,
but the server does not connect to it until Milestone 2. Stop the stack with:

```sh
docker compose -f deploy/compose/compose.yml down
```

Run the server with the environment in the [local quick start](../README.md).
Use `GET /healthz` for process liveness. `GET /readyz` performs a bounded Zoekt
health query and returns 503 with `{"error":"unavailable"}` when Zoekt is not
ready. `GET /metrics` exposes Prometheus metrics. Search and readiness are
bounded by the configuration caps documented in the README.

Keep Zoekt private. Compose joins the indexer, Zoekt, and PostgreSQL to an
internal network, publishing Zoekt only to `127.0.0.1:6070`. The pinned Zoekt
image runs as `linux/amd64`; this is deliberate for Apple-silicon hosts, where
Docker's emulation is needed because the pinned image has no arm64 variant.

`make image` and `make helm-lint` are expected to return nonzero with,
respectively, `image: milestone not implemented` and
`helm-lint: milestone not implemented`. They are boundaries, not failed release
checks for Milestones 0-1.

FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

ARG ZOEKT_VERSION
RUN test -n "$ZOEKT_VERSION"

WORKDIR /src
COPY go.mod go.sum ./
RUN GOWORK=off go mod download
COPY . .
# GOWORK=off: the module is self-contained; the workspace (go.work) would pull
# the optional scanner's tree-sitter dependencies into the image build.
RUN GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/graphnest-server ./cmd/graphnest-server && \
    GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/graphnest-admin ./cmd/graphnest-admin && \
    GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/graphnest-migrate ./cmd/graphnest-migrate && \
    GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/graphnest-mcp ./cmd/graphnest-mcp && \
    GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/graphnest-indexer ./cmd/graphnest-indexer
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-index@"$ZOEKT_VERSION" && \
    CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-webserver@"$ZOEKT_VERSION"

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS application

RUN apt-get update && \
    apt-get install --no-install-recommends -y ca-certificates wget && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /tmp /var/run/graphnest && \
    chgrp -R 0 /tmp /var/run/graphnest && \
    chmod -R g=u /tmp /var/run/graphnest
COPY --from=builder /out/graphnest-server /out/graphnest-admin /out/graphnest-migrate /out/graphnest-mcp /usr/local/bin/
USER 65532:0
EXPOSE 8080
CMD ["graphnest-server"]

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS node

RUN apt-get update && \
    apt-get install --no-install-recommends -y ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /data /tmp /var/run/graphnest && \
    chgrp -R 0 /data /tmp /var/run/graphnest && \
    chmod -R g=u /data /tmp /var/run/graphnest
COPY --from=builder /out/graphnest-indexer /out/zoekt-index /out/zoekt-webserver /usr/local/bin/
USER 65532:0
EXPOSE 6070 9090
CMD ["graphnest-indexer"]

# Compatibility-only image for legacy Git ingestion and native scanning.
FROM builder AS legacy-builder
RUN GOWORK=off go -C scanner mod download && \
    go -C scanner build -trimpath -ldflags="-s -w" -o /out/graphnest-scanner ./cmd/graphnest-scanner && \
    CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@"$ZOEKT_VERSION"

FROM node AS legacy-node
USER 0
RUN apt-get update && \
    apt-get install --no-install-recommends -y git libstdc++6 && \
    rm -rf /var/lib/apt/lists/*
COPY --from=legacy-builder /out/graphnest-scanner /out/zoekt-git-index /usr/local/bin/
USER 65532:0

FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

ARG ZOEKT_VERSION
RUN test -n "$ZOEKT_VERSION"

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/grepnest-server ./cmd/grepnest-server && \
    go build -trimpath -ldflags="-s -w" -o /out/grepnest-admin ./cmd/grepnest-admin && \
    go build -trimpath -ldflags="-s -w" -o /out/grepnest-migrate ./cmd/grepnest-migrate && \
    go build -trimpath -ldflags="-s -w" -o /out/grepnest-mcp ./cmd/grepnest-mcp && \
    go build -trimpath -ldflags="-s -w" -o /out/grepnest-indexer ./cmd/grepnest-indexer && \
    go build -trimpath -ldflags="-s -w" -o /out/grepnest-scanner ./cmd/grepnest-scanner
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-index@"$ZOEKT_VERSION" && \
    CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@"$ZOEKT_VERSION" && \
    CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-webserver@"$ZOEKT_VERSION"

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS application

RUN apt-get update && \
    apt-get install --no-install-recommends -y ca-certificates wget && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /tmp /var/run/grepnest && \
    chgrp -R 0 /tmp /var/run/grepnest && \
    chmod -R g=u /tmp /var/run/grepnest
COPY --from=builder /out/grepnest-server /out/grepnest-admin /out/grepnest-migrate /out/grepnest-mcp /usr/local/bin/
USER 65532:0
EXPOSE 8080
CMD ["grepnest-server"]

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS node

RUN apt-get update && \
    apt-get install --no-install-recommends -y ca-certificates git libstdc++6 && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /data /tmp /var/run/grepnest && \
    chgrp -R 0 /data /tmp /var/run/grepnest && \
    chmod -R g=u /data /tmp /var/run/grepnest
COPY --from=builder /out/grepnest-indexer /out/grepnest-scanner /out/zoekt-index /out/zoekt-git-index /out/zoekt-webserver /usr/local/bin/
USER 65532:0
EXPOSE 6070 9090
CMD ["grepnest-indexer"]

FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS builder

ARG ZOEKT_VERSION
RUN test -n "$ZOEKT_VERSION"

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/grepnest-server ./cmd/grepnest-server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/grepnest-admin ./cmd/grepnest-admin \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/grepnest-migrate ./cmd/grepnest-migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/grepnest-mcp ./cmd/grepnest-mcp \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/grepnest-indexer ./cmd/grepnest-indexer
RUN CGO_ENABLED=0 GOBIN=/out go install \
      github.com/sourcegraph/zoekt/cmd/zoekt-git-index@"$ZOEKT_VERSION" \
 && CGO_ENABLED=0 GOBIN=/out go install \
      github.com/sourcegraph/zoekt/cmd/zoekt-webserver@"$ZOEKT_VERSION"

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS application

RUN apt-get update \
 && apt-get install --no-install-recommends -y ca-certificates wget \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /tmp /var/run/grepnest \
 && chgrp -R 0 /tmp /var/run/grepnest \
 && chmod -R g=u /tmp /var/run/grepnest
COPY --from=builder /out/grepnest-server /out/grepnest-admin /out/grepnest-migrate /out/grepnest-mcp /usr/local/bin/
USER 65532:0
EXPOSE 8080
CMD ["grepnest-server"]

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS node

RUN apt-get update \
 && apt-get install --no-install-recommends -y ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /data /tmp /var/run/grepnest \
 && chgrp -R 0 /data /tmp /var/run/grepnest \
 && chmod -R g=u /data /tmp /var/run/grepnest
COPY --from=builder /out/grepnest-indexer /out/zoekt-git-index /out/zoekt-webserver /usr/local/bin/
USER 65532:0
EXPOSE 6070 9090
CMD ["grepnest-indexer"]

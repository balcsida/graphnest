FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

ARG ZOEKT_VERSION
ARG TARGETARCH
RUN test -n "$ZOEKT_VERSION" \
 && case "$TARGETARCH" in \
      amd64) archive_arch=x86_64; checksum=1fa1297620cd7bb05975ced5e41be751b236dae91244979d3502d39295655d70 ;; \
      arm64) archive_arch=aarch64; checksum=b2f41815b55c7e5b06bbbec8375b4bbd39567b767a2ee77c7dfa729c814737a7 ;; \
      *) exit 1 ;; \
    esac \
 && mkdir -p /opt/ladybug \
 && curl -fsSL "https://github.com/LadybugDB/ladybug/releases/download/v0.18.3/liblbug-linux-$archive_arch.tar.gz" \
      -o /tmp/ladybug.tar.gz \
 && echo "$checksum  /tmp/ladybug.tar.gz" | sha256sum -c - \
 && tar xzf /tmp/ladybug.tar.gz -C /opt/ladybug \
 && rm /tmp/ladybug.tar.gz

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=1 CGO_CFLAGS="-I/opt/ladybug" CGO_LDFLAGS="-L/opt/ladybug"
RUN go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-server ./cmd/grepnest-server \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-admin ./cmd/grepnest-admin \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-migrate ./cmd/grepnest-migrate \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-mcp ./cmd/grepnest-mcp \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-indexer ./cmd/grepnest-indexer \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-scanner ./cmd/grepnest-scanner \
 && go build -tags=system_ladybug -trimpath -ldflags="-s -w -extldflags=-Wl,-rpath,/usr/lib" \
    -o /out/grepnest-graph ./cmd/grepnest-graph
RUN CGO_ENABLED=0 GOBIN=/out go install \
      github.com/sourcegraph/zoekt/cmd/zoekt-index@"$ZOEKT_VERSION" \
 && CGO_ENABLED=0 GOBIN=/out go install \
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
 && apt-get install --no-install-recommends -y ca-certificates git libstdc++6 \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /data /tmp /var/run/grepnest \
 && chgrp -R 0 /data /tmp /var/run/grepnest \
 && chmod -R g=u /data /tmp /var/run/grepnest
COPY --from=builder /out/grepnest-indexer /out/grepnest-scanner /out/grepnest-graph /out/zoekt-index /out/zoekt-git-index /out/zoekt-webserver /usr/local/bin/
COPY --from=builder /opt/ladybug/liblbug.so* /usr/lib/
USER 65532:0
EXPOSE 6070 9090
CMD ["grepnest-indexer"]

ZOEKT_VERSION := v0.0.0-20260717095332-3c8b39b1ef4f

.PHONY: fmt lint test test-race integration e2e tools build server image helm-lint

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

lint:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go vet ./...; fi

test:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go test ./...; fi

test-race:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go test -race ./...; fi

integration:
	go test -tags=integration ./test/integration

tools:
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@$(ZOEKT_VERSION)
	GOBIN=$$(pwd)/.cache/bin go install github.com/sourcegraph/zoekt/cmd/zoekt-webserver@$(ZOEKT_VERSION)

e2e: tools
	ZOEKT_GIT_INDEX=$$(pwd)/.cache/bin/zoekt-git-index ZOEKT_WEBSERVER=$$(pwd)/.cache/bin/zoekt-webserver go test -v -tags=e2e ./test/e2e

build:
	@if test -n "$$(go list ./cmd/... 2>/dev/null)"; then go build ./cmd/...; fi

server:
	go run ./cmd/grepnest-server

image:
	@echo "image: milestone not implemented" >&2
	@false

helm-lint:
	@echo "helm-lint: milestone not implemented" >&2
	@false

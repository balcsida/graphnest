.PHONY: fmt lint test test-race integration build image helm-lint

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

build:
	@if test -n "$$(go list ./cmd/... 2>/dev/null)"; then go build ./cmd/...; fi

image:
	@echo "image: milestone not implemented" >&2
	@false

helm-lint:
	@echo "helm-lint: milestone not implemented" >&2
	@false

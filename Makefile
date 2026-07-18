.PHONY: fmt lint test test-race integration e2e build image helm-lint

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

lint:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go vet ./...; fi

test:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go test ./...; fi

test-race:
	@if test -n "$$(go list ./... 2>/dev/null)"; then go test -race ./...; fi

integration:
	@echo "integration: milestone not implemented" >&2
	@false

e2e:
	@echo "e2e: milestone not implemented" >&2
	@false

build:
	@if test -n "$$(go list ./cmd/... 2>/dev/null)"; then go build ./cmd/...; fi

image:
	@echo "image: milestone not implemented" >&2
	@false

helm-lint:
	@echo "helm-lint: milestone not implemented" >&2
	@false

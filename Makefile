ZOEKT_VERSION := v0.0.0-20260717095332-3c8b39b1ef4f
POSTGRES_COMPOSE := docker compose -p grepnest-postgres
GREPNEST_TEST_POSTGRES_DSN ?= $(GREPNEST_TEST_DATABASE_URL)

.PHONY: fmt lint test test-race integration postgres-test postgres-integration e2e e2e-test tools build server image helm-lint helm-test

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

postgres-test:
	GREPNEST_TEST_POSTGRES_DSN='$(GREPNEST_TEST_POSTGRES_DSN)' go test -count=1 -tags=integration ./internal/postgres ./test/integration

postgres-integration:
	$(POSTGRES_COMPOSE) -f deploy/compose/compose.yml up -d --wait postgres
	@address="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml port postgres 5432)"; \
	case "$$address" in ""|"invalid IP:0") \
		container="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml ps -q postgres)"; \
		address="$$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$$container")"; \
		test -n "$$address"; \
		address="$$address:5432" ;; \
	esac; \
	$(MAKE) postgres-test GREPNEST_TEST_POSTGRES_DSN="postgres://grepnest:grepnest@$$address/grepnest?sslmode=disable"

tools:
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@$(ZOEKT_VERSION)
	GOBIN=$$(pwd)/.cache/bin go install github.com/sourcegraph/zoekt/cmd/zoekt-webserver@$(ZOEKT_VERSION)

e2e: tools
	$(POSTGRES_COMPOSE) -f deploy/compose/compose.yml up -d --wait postgres
	@address="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml port postgres 5432)"; \
	case "$$address" in ""|"invalid IP:0") \
		container="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml ps -q postgres)"; \
		address="$$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$$container")"; \
		test -n "$$address"; \
		address="$$address:5432" ;; \
	esac; \
	$(MAKE) e2e-test GREPNEST_TEST_POSTGRES_DSN="postgres://grepnest:grepnest@$$address/grepnest?sslmode=disable"

e2e-test:
	GREPNEST_TEST_POSTGRES_DSN='$(GREPNEST_TEST_POSTGRES_DSN)' GREPNEST_REQUIRE_POSTGRES=1 ZOEKT_GIT_INDEX=$$(pwd)/.cache/bin/zoekt-git-index ZOEKT_WEBSERVER=$$(pwd)/.cache/bin/zoekt-webserver go test -v -tags=e2e ./test/e2e

build:
	@if test -n "$$(go list ./cmd/... 2>/dev/null)"; then go build ./cmd/...; fi

server:
	go run ./cmd/grepnest-server

image:
	@echo "image: milestone not implemented" >&2
	@false

helm-lint:
	helm lint deploy/helm/grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml

helm-test:
	sh deploy/helm/grepnest/tests/render.sh

ZOEKT_VERSION := v0.0.0-20260717095332-3c8b39b1ef4f
STATICCHECK_VERSION := v0.7.0
GOVULNCHECK_VERSION := v1.1.4
POSTGRES_COMPOSE := docker compose -p grepnest-postgres
GREPNEST_TEST_POSTGRES_DSN ?= $(GREPNEST_TEST_DATABASE_URL)
IMAGE_PLATFORM ?= linux/amd64
APPLICATION_IMAGE ?= grepnest-application:dev
NODE_IMAGE ?= grepnest-node:dev

.PHONY: fmt lint staticcheck govulncheck test test-race makefile-test scanner-test abi-test integration postgres-test postgres-integration e2e e2e-test tools build server image image-test zoekt-version helm-lint helm-test compose-test openapi-check release-chart-test tools-check ui-smoke

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

lint:
	go vet ./...

staticcheck:
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	.cache/bin/staticcheck ./...

govulncheck:
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	.cache/bin/govulncheck ./...

test:
	go test ./...

test-race:
	go test -race ./...

tools-check:
	@tool=$$(cd tools && go tool -n buf); \
	"$$tool" generate; \
	git diff --exit-code -- internal/graphartifact/v1/artifact.pb.go

makefile-test:
	@for target in lint test test-race build; do \
		if GOFLAGS=-definitely-invalid $(MAKE) --no-print-directory $$target >/dev/null 2>&1; then \
			echo "$$target ignored a Go command failure" >&2; exit 1; \
		fi; \
	done

scanner-test:
	CGO_ENABLED=1 go test -race ./internal/graphscan/... ./internal/graphscanner ./cmd/grepnest-scanner

abi-test:
	CGO_ENABLED=1 go test ./internal/graphscan -run '^TestGrammarMatrix$$' -count=1

openapi-check:
	ruby scripts/check_openapi.rb

integration: postgres-integration

postgres-test:
	GREPNEST_TEST_POSTGRES_DSN='$(GREPNEST_TEST_POSTGRES_DSN)' go test -count=1 -tags=integration ./internal/postgres ./internal/authz ./internal/webhook ./test/integration ./cmd/grepnest-indexer

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
	GOBIN=$$(pwd)/.cache/bin go install github.com/sourcegraph/zoekt/cmd/zoekt-index@$(ZOEKT_VERSION)
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
	GREPNEST_TEST_POSTGRES_DSN='$(GREPNEST_TEST_POSTGRES_DSN)' GREPNEST_REQUIRE_POSTGRES=1 ZOEKT_INDEX=$$(pwd)/.cache/bin/zoekt-index ZOEKT_GIT_INDEX=$$(pwd)/.cache/bin/zoekt-git-index ZOEKT_WEBSERVER=$$(pwd)/.cache/bin/zoekt-webserver go test -v -tags=e2e ./test/e2e

build:
	go build ./cmd/...

server:
	go run ./cmd/grepnest-server

zoekt-version:
	@printf '%s\n' '$(ZOEKT_VERSION)'

image:
	docker buildx build --load --platform $(IMAGE_PLATFORM) --target application \
		--build-arg ZOEKT_VERSION=$(ZOEKT_VERSION) -t $(APPLICATION_IMAGE) .
	docker buildx build --load --platform $(IMAGE_PLATFORM) --target node \
		--build-arg ZOEKT_VERSION=$(ZOEKT_VERSION) -t $(NODE_IMAGE) .

image-test: image
	APPLICATION_IMAGE=$(APPLICATION_IMAGE) NODE_IMAGE=$(NODE_IMAGE) \
		sh deploy/images/test.sh

helm-lint:
	helm lint deploy/helm/grepnest -f deploy/helm/grepnest/ci/minimal-values.yaml

helm-test:
	sh deploy/helm/grepnest/tests/render.sh

release-chart-test:
	ruby scripts/stage_release_chart_test.rb

compose-test:
	sh deploy/compose/test.sh

ui-smoke: tools
	sh test/smoke/public_ui.sh
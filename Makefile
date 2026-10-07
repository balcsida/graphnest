ZOEKT_VERSION = $(shell GOWORK=off go -C tools list -m -f '{{.Version}}' github.com/sourcegraph/zoekt)
STATICCHECK_VERSION := v0.8.1
GOVULNCHECK_VERSION := v1.8.0
POSTGRES_COMPOSE := docker compose -p graphnest-postgres
GRAPHNEST_TEST_POSTGRES_DSN ?= $(GRAPHNEST_TEST_DATABASE_URL)
VERSION ?= dev
CLI_VERSION_FLAG = -X github.com/balcsida/graphnest/internal/cli.Version=$(VERSION)
CLI_TARGETS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
IMAGE_PLATFORM ?= linux/amd64
APPLICATION_IMAGE ?= graphnest-application:dev
NODE_IMAGE ?= graphnest-node:dev
WEB_INPUTS = web/package.json web/package-lock.json web/vite.config.ts web/index.html web/components.json \
	$(wildcard web/tsconfig*.json) $(shell find web/src web/public -type f)

.PHONY: brand-check fmt lint staticcheck govulncheck test test-race makefile-test scanner-build scanner-test scanner-vulncheck abi-test integration postgres-test postgres-integration e2e e2e-test tools build cli server image image-test zoekt-version helm-lint helm-test compose-test openapi-check release-chart-test tools-check ui-smoke ui ui-check ui-dev ui-screenshots

brand-check:
	@status=0; git grep -I -i -E 'grep[-_]?nest|graph[-_]nest' -- . || status=$$?; test $$status -eq 1
	@paths=$$(git ls-files) || exit $$?; \
	status=0; printf '%s\n' "$$paths" | grep -Eiq 'grep[-_]?nest|graph[-_]nest' || status=$$?; \
	test $$status -eq 1

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*' -not -path './web/*'))"

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

test: ui
	CGO_ENABLED=0 go test ./...

test-race: ui
	go test -race ./...

tools-check:
	@tool=$$(cd tools && go tool -n buf); \
	"$$tool" generate; \
	git diff --exit-code

makefile-test:
	@for target in lint test test-race build; do \
		if GOFLAGS=-definitely-invalid $(MAKE) --no-print-directory $$target >/dev/null 2>&1; then \
			echo "$$target ignored a Go command failure" >&2; exit 1; \
		fi; \
	done

scanner-build:
	go -C scanner build ./...

scanner-test:
	CGO_ENABLED=1 go -C scanner test -race ./...

scanner-vulncheck:
	go -C scanner run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

abi-test:
	CGO_ENABLED=1 go -C scanner test ./graphscan -run '^TestGrammarMatrix$$' -count=1

openapi-check:
	ruby scripts/check_openapi.rb

integration: postgres-integration

postgres-test:
	@fixture="$$(mktemp "$$(pwd)/.graphnest-codegraph-v2.XXXXXX")" || exit; \
	trap 'status=$$?; rm -f "$$fixture"; exit $$status' EXIT; \
	trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM; \
	GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE="$$fixture" go test -count=1 ./internal/graphartifact -run '^TestV2ExportQueryFixture$$' && \
	GRAPHNEST_TEST_CODEGRAPH_V2_FIXTURE="$$fixture" GRAPHNEST_TEST_POSTGRES_DSN='$(GRAPHNEST_TEST_POSTGRES_DSN)' go test -count=1 -tags=integration ./internal/postgres ./internal/authz ./internal/webhook ./test/integration ./cmd/graphnest-indexer ./cmd/graphnest-server

postgres-integration:
	$(POSTGRES_COMPOSE) -f deploy/compose/compose.yml up -d --wait postgres
	@address="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml port postgres 5432)"; \
	case "$$address" in ""|"invalid IP:0") \
		container="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml ps -q postgres)"; \
		address="$$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$$container")"; \
		test -n "$$address"; \
		address="$$address:5432" ;; \
	esac; \
	$(MAKE) postgres-test GRAPHNEST_TEST_POSTGRES_DSN="postgres://graphnest:graphnest@$$address/graphnest?sslmode=disable"

tools:
	mkdir -p .cache/bin
	GOWORK=off GOBIN=$$(pwd)/.cache/bin go -C tools install github.com/sourcegraph/zoekt/cmd/zoekt-index
	GOWORK=off GOBIN=$$(pwd)/.cache/bin go -C tools install github.com/sourcegraph/zoekt/cmd/zoekt-git-index
	GOWORK=off GOBIN=$$(pwd)/.cache/bin go -C tools install github.com/sourcegraph/zoekt/cmd/zoekt-webserver

e2e: tools
	$(POSTGRES_COMPOSE) -f deploy/compose/compose.yml up -d --wait postgres
	@address="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml port postgres 5432)"; \
	case "$$address" in ""|"invalid IP:0") \
		container="$$($(POSTGRES_COMPOSE) -f deploy/compose/compose.yml ps -q postgres)"; \
		address="$$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$$container")"; \
		test -n "$$address"; \
		address="$$address:5432" ;; \
	esac; \
	$(MAKE) e2e-test GRAPHNEST_TEST_POSTGRES_DSN="postgres://graphnest:graphnest@$$address/graphnest?sslmode=disable"

e2e-test:
	GRAPHNEST_TEST_POSTGRES_DSN='$(GRAPHNEST_TEST_POSTGRES_DSN)' GRAPHNEST_REQUIRE_POSTGRES=1 ZOEKT_INDEX=$$(pwd)/.cache/bin/zoekt-index ZOEKT_GIT_INDEX=$$(pwd)/.cache/bin/zoekt-git-index ZOEKT_WEBSERVER=$$(pwd)/.cache/bin/zoekt-webserver go test -v -tags=e2e ./test/e2e
	GRAPHNEST_TEST_POSTGRES_DSN='$(GRAPHNEST_TEST_POSTGRES_DSN)' GRAPHNEST_REQUIRE_POSTGRES=1 CGO_ENABLED=1 go -C scanner test -v -tags=e2e ./test/e2e

web/node_modules/.package-lock.json: web/package-lock.json
	npm --prefix web ci
	touch $@

internal/webui/dist/index.html: web/node_modules/.package-lock.json $(WEB_INPUTS)
	npm --prefix web run build

ui: internal/webui/dist/index.html

ui-check: web/node_modules/.package-lock.json
	npm --prefix web run check
	npm --prefix web test

ui-dev: web/node_modules/.package-lock.json
	npm --prefix web run dev

ui-screenshots: ui
	node test/smoke/console-screenshots.mjs docs/images

build: ui
	go build ./cmd/...

cli:
	rm -rf dist/cli
	mkdir -p dist/cli
	@set -e; for target in $(CLI_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building graphnest $(VERSION) for $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w $(CLI_VERSION_FLAG)" \
			-o dist/cli/graphnest_$(VERSION)_$${os}_$${arch} ./cmd/graphnest; \
	done
	cd dist/cli && shasum -a 256 graphnest_$(VERSION)_linux_* graphnest_$(VERSION)_darwin_* >graphnest_$(VERSION)_checksums.txt
	go version -m dist/cli/graphnest_$(VERSION)_linux_amd64 >dist/cli/graphnest_$(VERSION)_dependencies.txt

server: ui
	go run ./cmd/graphnest-server

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
	helm lint deploy/helm/graphnest -f deploy/helm/graphnest/ci/minimal-values.yaml

helm-test:
	sh deploy/helm/graphnest/tests/render.sh

release-chart-test:
	ruby scripts/stage_release_chart_test.rb

compose-test:
	sh deploy/compose/test.sh

ui-smoke: tools ui
	sh test/smoke/public_ui.sh

.PHONY: parity-reference
parity-reference:
	python3 -m unittest discover -s test/parity -p 'test_*.py'

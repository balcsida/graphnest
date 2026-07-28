ZOEKT_VERSION := v0.0.0-20260717095332-3c8b39b1ef4f
STATICCHECK_VERSION := v0.7.0
GOVULNCHECK_VERSION := v1.1.4
POSTGRES_COMPOSE := docker compose -p grepnest-postgres
GREPNEST_TEST_POSTGRES_DSN ?= $(GREPNEST_TEST_DATABASE_URL)
IMAGE_PLATFORM ?= linux/amd64
APPLICATION_IMAGE ?= grepnest-application:dev
NODE_IMAGE ?= grepnest-node:dev
LADYBUG_VERSION := 0.18.3
LADYBUG_LIB_DIR ?= $(CURDIR)/.cache/ladybug/v$(LADYBUG_VERSION)
LADYBUG_OS ?= $(shell uname -s)
LADYBUG_ARCH ?= $(shell uname -m)

ifeq ($(LADYBUG_OS)/$(LADYBUG_ARCH),Darwin/arm64)
LADYBUG_LIBRARY := liblbug.dylib
LADYBUG_ARCHIVE := liblbug-osx-arm64.tar.gz
LADYBUG_ARCHIVE_SHA256 := f626987fe10f6520146793575677d004962b4c6a0dea71cbbca75e73ab673622
LADYBUG_RUNTIME_ENV := DYLD_LIBRARY_PATH=$(LADYBUG_LIB_DIR)
else ifeq ($(LADYBUG_OS)/$(LADYBUG_ARCH),Linux/x86_64)
LADYBUG_LIBRARY := liblbug.so
LADYBUG_ARCHIVE := liblbug-linux-x86_64.tar.gz
LADYBUG_ARCHIVE_SHA256 := 1fa1297620cd7bb05975ced5e41be751b236dae91244979d3502d39295655d70
LADYBUG_RUNTIME_ENV := LD_LIBRARY_PATH=$(LADYBUG_LIB_DIR)
else
$(error unsupported Ladybug platform: $(LADYBUG_OS)/$(LADYBUG_ARCH))
endif

LADYBUG_ARCHIVE_URL := https://github.com/LadybugDB/ladybug/releases/download/v$(LADYBUG_VERSION)/$(LADYBUG_ARCHIVE)
LADYBUG_ENV := CGO_ENABLED=1 LBUG_VERSION=$(LADYBUG_VERSION) GOCACHE=$(CURDIR)/.cache/go-build XDG_CACHE_HOME=$(CURDIR)/.cache $(LADYBUG_RUNTIME_ENV) CGO_CFLAGS="-I$(LADYBUG_LIB_DIR)" CGO_LDFLAGS="-L$(LADYBUG_LIB_DIR)"
LADYBUG_GO := $(LADYBUG_ENV) go
LADYBUG_TAGS := -tags=system_ladybug
LADYBUG_RPATH := -ldflags=-extldflags=-Wl,-rpath,$(LADYBUG_LIB_DIR)

.PHONY: fmt lint staticcheck govulncheck test test-race scanner-test ladybug-test integration postgres-test postgres-integration e2e e2e-test tools build server image image-test zoekt-version helm-lint helm-test compose-test openapi-check release-chart-test

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.cache/*'))"

lint: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	@if test -n "$$($(LADYBUG_GO) list $(LADYBUG_TAGS) ./... 2>/dev/null)"; then $(LADYBUG_GO) vet $(LADYBUG_TAGS) ./...; fi

staticcheck: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	$(LADYBUG_ENV) .cache/bin/staticcheck $(LADYBUG_TAGS) ./...

govulncheck:
	mkdir -p .cache/bin
	GOBIN=$$(pwd)/.cache/bin go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	.cache/bin/govulncheck ./...

test: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	@if test -n "$$($(LADYBUG_GO) list $(LADYBUG_TAGS) ./... 2>/dev/null)"; then $(LADYBUG_GO) test $(LADYBUG_TAGS) ./...; fi

test-race: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	@if test -n "$$($(LADYBUG_GO) list $(LADYBUG_TAGS) ./... 2>/dev/null)"; then $(LADYBUG_GO) test -race $(LADYBUG_TAGS) ./...; fi

scanner-test:
	CGO_ENABLED=1 go test -race ./internal/graphscan/... ./internal/graphscanner ./cmd/grepnest-scanner

ladybug-test: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	$(LADYBUG_GO) test $(LADYBUG_TAGS) ./internal/ladybug ./internal/graphquery ./internal/graphruntime

$(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY):
	mkdir -p $(LADYBUG_LIB_DIR)
	env -u HTTPS_PROXY -u HTTP_PROXY -u https_proxy -u http_proxy curl -fsSL $(LADYBUG_ARCHIVE_URL) -o $(LADYBUG_LIB_DIR)/$(LADYBUG_ARCHIVE)
	echo "$(LADYBUG_ARCHIVE_SHA256)  $(LADYBUG_LIB_DIR)/$(LADYBUG_ARCHIVE)" | shasum -a 256 -c -
	tar xzf $(LADYBUG_LIB_DIR)/$(LADYBUG_ARCHIVE) -C $(LADYBUG_LIB_DIR)
ifeq ($(LADYBUG_OS),Darwin)
	ln -sf liblbug.$(LADYBUG_VERSION).dylib $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
else
	test -f $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
endif
	rm $(LADYBUG_LIB_DIR)/$(LADYBUG_ARCHIVE)

openapi-check:
	ruby scripts/check_openapi.rb

integration: postgres-integration

postgres-test:
	GREPNEST_TEST_POSTGRES_DSN='$(GREPNEST_TEST_POSTGRES_DSN)' go test -count=1 -tags=integration ./internal/postgres ./internal/authz ./internal/webhook ./test/integration

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

build: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	@if test -n "$$($(LADYBUG_GO) list $(LADYBUG_TAGS) ./cmd/... 2>/dev/null)"; then $(LADYBUG_GO) build $(LADYBUG_TAGS) $(LADYBUG_RPATH) ./cmd/...; fi

server: $(LADYBUG_LIB_DIR)/$(LADYBUG_LIBRARY)
	$(LADYBUG_GO) run $(LADYBUG_TAGS) ./cmd/grepnest-server

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

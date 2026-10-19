GO            ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK   ?= govulncheck

BIN_DIR := bin
PKG     := github.com/meister119/leasorix

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)

NPM ?= npm
IMAGE ?= leasorix
DOCKER_BUILD_ARGS ?=

.PHONY: all ci build build-go build-noui dist docker ui ui-test generate test test-integration test-e2e test-dns screenshots fuzz lint vuln fmt tidy clean

all: ci

## ci: everything the CI pipeline runs
ci: lint test ui-test build vuln

## build: build the web UI and the static binary with the UI embedded
build: ui build-go

## build-go: build leasorixd with the UI from web/dist (run `make ui` first) and leasorixctl
build-go:
	CGO_ENABLED=0 $(GO) build -tags ui -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leasorixd ./cmd/leasorixd
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leasorixctl ./cmd/leasorixctl

## build-noui: binary without web UI (no Node.js needed)
build-noui:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leasorixd ./cmd/leasorixd
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leasorixctl ./cmd/leasorixctl

## dist: release files in dist/ (run `make ui` first): per platform an archive
## with leasorixd (UI embedded), leasorixctl, the example configuration and the
## systemd unit; the Compose files without a build section, pinned to
## $(DIST_IMAGE):$(DIST_IMAGE_TAG); SHA256SUMS
DIST_DIR       := dist
DIST_PLATFORMS ?= linux/amd64 linux/arm64
DIST_IMAGE     ?= ghcr.io/meister119/leasorix
DIST_IMAGE_TAG ?= latest
dist:
	@test -f web/dist/index.html || { echo "web/dist is missing: run 'make ui' first" >&2; exit 1; }
	rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	@set -e; for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=leasorix_$(VERSION:v%=%)_$${os}_$${arch}; dir=$(DIST_DIR)/$$name; \
		echo "building $$name"; mkdir -p $$dir; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -tags ui -trimpath -ldflags "$(LDFLAGS)" -o $$dir/leasorixd ./cmd/leasorixd; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/leasorixctl ./cmd/leasorixctl; \
		cp README.md deploy/examples/leasorix.yml deploy/systemd/leasorixd.service $$dir/; \
		tar -C $(DIST_DIR) --sort=name --owner=0 --group=0 --numeric-owner -czf $$dir.tar.gz $$name; \
		rm -rf $$dir; \
	done
	@for f in deploy/docker-compose*.yml; do \
		sed -e '/^ *build:$$/,/^ *dockerfile: deploy\/Dockerfile$$/d' \
			-e 's#ghcr.io/meister119/leasorix:latest#$(DIST_IMAGE):$(DIST_IMAGE_TAG)#' $$f > $(DIST_DIR)/$${f##*/}; \
	done
	cd $(DIST_DIR) && sha256sum *.tar.gz *.yml > SHA256SUMS

## docker: build the container image $(IMAGE):$(VERSION) and :latest
## (DOCKER_BUILD_ARGS e.g. "--build-arg GO_IMAGE=mirror.gcr.io/library/golang:1.27-bookworm")
docker:
	docker build -f deploy/Dockerfile $(DOCKER_BUILD_ARGS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

## ui: install frontend dependencies and build web/dist
ui:
	cd web && $(NPM) ci --no-audit --no-fund && $(NPM) run build

## ui-test: frontend type check and unit tests
ui-test:
	cd web && $(NPM) run typecheck && $(NPM) test

## generate: regenerate API code (openapi.yaml) and the HA peer protocol (protoc 3.21 required)
generate:
	$(GO) build -o bin/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go
	$(GO) build -o bin/protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc
	$(GO) generate ./internal/api/... ./internal/ha/pb/...
	cd web && $(NPM) run gen

## test: run unit tests with the race detector
test:
	$(GO) test -race -count=1 ./...

## test-integration: netns based end-to-end tests (requires root, iproute2,
## dhclient, udhcpc, dnsmasq)
test-integration:
	$(GO) test -tags integration -count=1 -v -timeout 20m ./test/integration/...

## test-docker: test the image and the compose files (host, macvlan, HA; root, docker)
test-docker:
	IMAGE=$(IMAGE):latest ./test/docker/run.sh

## test-ha: two-node HA tests: load balance with 5000 clients, failover,
## partition and configuration sync (requires root, iproute2)
test-ha:
	$(GO) test -tags integration -count=1 -v -timeout 15m -run 'TestHA' ./test/integration/...

## test-dns: dynamic DNS updates against BIND and PowerDNS in Docker (root, docker compose)
test-dns:
	$(GO) test -tags integration -count=1 -v -timeout 10m -run 'TestDNS' ./test/integration/...

## test-e2e: Playwright end-to-end tests against a real server in network
## namespaces (requires root, a built binary and the web UI)
test-e2e: build
	./test/e2e/run.sh

## screenshots: regenerate the README screenshots in docs/images (root,
## like test-e2e; demo data from test/screenshots/demo.yaml)
screenshots: build
	mkdir -p docs/images
	SCREENSHOTS=1 ./test/e2e/run.sh e2e/screenshots.spec.ts

## fuzz: fuzz the DHCPv4 and DHCPv6 packet paths and the FQDN option parser
FUZZTIME ?= 60s
fuzz:
	$(GO) test -run '^$$' -fuzz FuzzHandle -fuzztime $(FUZZTIME) ./internal/dhcp4
	$(GO) test -run '^$$' -fuzz FuzzParseClientFQDN -fuzztime $(FUZZTIME) ./internal/dnsupdate
	$(GO) test -run '^$$' -fuzz FuzzHandle6 -fuzztime $(FUZZTIME) ./internal/dhcp6

## lint: run golangci-lint
lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags integration ./test/...

## vuln: scan dependencies for known vulnerabilities
vuln:
	$(GOVULNCHECK) ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

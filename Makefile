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

.PHONY: all ci build test test-race lint vuln fmt tidy clean

all: ci

## ci: everything the CI pipeline runs
ci: lint test build vuln

## build: build static binaries into ./bin
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/leasorixd ./cmd/leasorixd

## test: run unit tests with the race detector
test:
	$(GO) test -race -count=1 ./...

## lint: run golangci-lint
lint:
	$(GOLANGCI_LINT) run ./...

## vuln: scan dependencies for known vulnerabilities
vuln:
	$(GOVULNCHECK) ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)

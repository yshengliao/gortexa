GO ?= go
GOBIN := $(shell $(GO) env GOPATH)/bin

# Override the container's broken GOPRIVATE/GOPROXY for every recipe shell
# (Make-exported vars win over inherited OS env). See install.sh for the why.
export GOFLAGS := -mod=mod
export GOPROXY := https://proxy.golang.org,direct
export GOSUMDB := sum.golang.org
export GOTOOLCHAIN := auto
export GOPRIVATE :=
export GOINSECURE :=
export PATH := $(GOBIN):$(PATH)

.PHONY: bootstrap gen sqlc build test test-integration cover lint vet vuln tidy clean run skills

bootstrap:
	bash install.sh

# gen is the contract-first pipeline: lint -> breaking -> generate. It uses the
# committed buf.lock (run `buf dep update` manually to bump proto deps); the
# breaking gate runs against the local default branch when present (skipped in a
# shallow CI checkout that lacks it).
gen:
	buf lint
	@if git rev-parse --verify --quiet main >/dev/null 2>&1; then buf breaking --against '.git#branch=main'; else echo "==> skipping buf breaking (no 'main' ref)"; fi
	buf generate

sqlc:
	sqlc generate

build:
	$(GO) build ./...

test:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags integration ./...

cover:
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	@grep -vE '/gen/|/cmd/' coverage.out > coverage.filtered.out || true
	$(GO) tool cover -func=coverage.filtered.out | tail -1

lint:
	golangci-lint run ./...

vet:
	$(GO) vet ./...

vuln:
	govulncheck ./... || true

tidy:
	$(GO) mod tidy

skills:
	bash install-skills.sh

clean:
	rm -rf gen bin coverage*.out

run:
	$(GO) run ./cmd/server

.PHONY: dev dev-down

dev:
	docker compose -f deploy/docker-compose.yaml up -d
	@echo "Jaeger UI:   http://localhost:16686"
	@echo "Grafana:     http://localhost:3000"
	@echo "Prometheus:  http://localhost:9090"

dev-down:
	docker compose -f deploy/docker-compose.yaml down

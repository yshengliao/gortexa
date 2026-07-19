GO ?= go
GOBIN := $(shell $(GO) env GOPATH)/bin

# Override the container's broken GOPRIVATE/GOPROXY for every recipe shell
# (Make-exported vars win over inherited OS env). See install.sh in the framework repo for the why.
export GOFLAGS := -mod=mod
export GOPROXY := https://proxy.golang.org,direct
export GOSUMDB := sum.golang.org
export GOTOOLCHAIN := auto
export GOPRIVATE :=
export GOINSECURE :=
export PATH := $(GOBIN):$(PATH)

.PHONY: bootstrap gen sqlc build test test-integration cover bench lint vet vuln tidy clean run skills

bootstrap:
	@if [ -f install.sh ]; then \
		bash install.sh; \
	else \
		echo "==> no install.sh (scaffolded project): installing pinned tools from tools/go.mod"; \
		go -C tools install tool; \
	fi

# gen is the contract-first pipeline: lint -> breaking -> generate. It uses the
# committed buf.lock (run `buf dep update` manually to bump proto deps); the
# breaking gate prefers origin/main (what CI fetches — comparing against the
# local branch would self-compare on push builds), falls back to a local main,
# and is skipped only when neither ref exists.
gen:
	buf lint
	@if git rev-parse --verify --quiet origin/main >/dev/null 2>&1; then \
		buf breaking --against '.git#ref=origin/main'; \
	elif git rev-parse --verify --quiet main >/dev/null 2>&1; then \
		buf breaking --against '.git#branch=main'; \
	else \
		echo "==> skipping buf breaking (no main ref)"; \
	fi
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

# bench runs every microbenchmark and summarizes with benchstat. allocs/op and
# B/op are machine-independent; ns/op depends on the host. bench.out is
# gitignored. `make bootstrap` installs benchstat (tools/go.mod).
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -count=8 ./... | tee bench.out
	benchstat bench.out

lint:
	golangci-lint run ./...

vet:
	$(GO) vet ./...

vuln:
	govulncheck ./...
	govulncheck -tags integration ./...

tidy:
	$(GO) mod tidy

skills:
	bash install-skills.sh

clean:
	rm -rf gen bin coverage*.out

run:
	@# The committed etc/config.yaml jwt_secret is a placeholder the server now
	@# refuses to boot with; inject a local-only dev secret so `make run` works.
	@# NEVER use this value in a deployment — set GORTEXA_AUTH__JWT_SECRET there.
	GORTEXA_AUTH__JWT_SECRET="local-dev-only-secret-not-for-production-0123456789" $(GO) run ./cmd/server

.PHONY: dev dev-down

dev:
	docker compose -f deploy/docker-compose.yaml up -d
	@echo "Jaeger UI:   http://localhost:16686"
	@echo "Grafana:     http://localhost:3000"
	@echo "Prometheus:  http://localhost:9090"

dev-down:
	docker compose -f deploy/docker-compose.yaml down

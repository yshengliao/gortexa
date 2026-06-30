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

gen:
	buf dep update
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

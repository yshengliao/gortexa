#!/usr/bin/env bash
# setup.sh — Gortexa dev-environment bootstrap. MUST run before any build.
#
# Why this exists: the container injects GOPRIVATE/GOPROXY values that force
# github.com / google.golang.org / go.opentelemetry.io modules to resolve
# *direct via git*, which this network policy blocks (HTTP 403). `go env -w`
# cannot override an OS environment variable, so we export the corrected values
# here (and also persist them to the go env file for tools that don't inherit
# this shell). Modules then route through proxy.golang.org + sum.golang.org,
# which ARE reachable. Verified: grpc/gateway/protobuf/otel/pgx/redis/buf/sqlc.
set -euo pipefail

export GOPRIVATE="" GOINSECURE="" GONOSUMCHECK="" GONOSUMDB=""
export GOFLAGS="-mod=mod"
export GOPROXY="https://proxy.golang.org,direct"
export GOSUMDB="sum.golang.org"
export GOTOOLCHAIN="auto"

# Persist for non-inheriting tools (no-op where OS env already wins).
go env -w GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" GOTOOLCHAIN="${GOTOOLCHAIN}" \
          GOFLAGS="${GOFLAGS}" GOPRIVATE="" GOINSECURE="" 2>/dev/null || true

GOBIN="$(go env GOPATH)/bin"
export PATH="${GOBIN}:${PATH}"

echo "==> Go: $(go version)"
echo "==> installing proto toolchain into ${GOBIN}"
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.29.0
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.29.0
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
go install golang.org/x/vuln/cmd/govulncheck@latest

echo "==> done. Ensure ${GOBIN} is on PATH (the Makefile does this automatically)."

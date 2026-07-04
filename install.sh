#!/usr/bin/env bash
# install.sh — Gortexa dev-environment bootstrap. Homebrew-style: safe to run
# from a checkout (./install.sh) or piped straight from the web:
#
#   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
#
# It corrects the Go module env, then installs the pinned dev toolchain declared
# as go.mod `tool` directives in tools/go.mod (buf + protoc plugins + sqlc +
# govulncheck + benchstat) via `go install tool`. Idempotent: re-running just
# reinstalls the same pinned versions.
set -euo pipefail

# --- Corrected Go env -------------------------------------------------------
# The dev container ships GOPRIVATE/GOPROXY values that force github.com /
# google.golang.org / go.opentelemetry.io to resolve direct-via-git, which the
# network policy 403s. `go env -w` cannot override an OS env var, so export the
# corrected values for this process (and persist them for tools that don't
# inherit this shell). Modules then route through proxy.golang.org + sum.golang.org.
export GOPRIVATE="" GOINSECURE="" GONOSUMCHECK="" GONOSUMDB=""
export GOFLAGS="-mod=mod"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

# --- Preconditions ----------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go is not installed. Install Go >= 1.26 from https://go.dev/dl/ then re-run." >&2
  exit 1
fi
go env -w GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" GOTOOLCHAIN="${GOTOOLCHAIN}" \
          GOFLAGS="${GOFLAGS}" GOPRIVATE="" GOINSECURE="" 2>/dev/null || true

REPO_URL="${GORTEXA_REPO:-https://github.com/yshengliao/gortexa}"
REPO_REF="${GORTEXA_REF:-main}"

# --- Locate (or clone) the gortexa module root ------------------------------
# If run inside a checkout, use it; otherwise clone (the curl|bash one-liner).
find_root() {
  local d="${PWD}"
  while [ "${d}" != "/" ]; do
    if [ -f "${d}/go.mod" ] && grep -q '^module github.com/yshengliao/gortexa$' "${d}/go.mod" 2>/dev/null; then
      printf '%s\n' "${d}"
      return 0
    fi
    d="$(dirname "${d}")"
  done
  return 1
}

if ROOT="$(find_root)"; then
  echo "==> using gortexa checkout at ${ROOT}"
else
  if ! command -v git >/dev/null 2>&1; then
    echo "ERROR: git is required to clone gortexa. Install git or run from a checkout." >&2
    exit 1
  fi
  DEST="${GORTEXA_DIR:-gortexa}"
  echo "==> cloning ${REPO_URL} (${REPO_REF}) into ${DEST}"
  # "--" ends option parsing so a REPO_URL/DEST beginning with "-" can't be read
  # as a git flag (argument injection), matching `gortexa create`.
  git clone --depth 1 --branch "${REPO_REF}" -- "${REPO_URL}" "${DEST}"
  ROOT="$(cd "${DEST}" && pwd)"
fi
cd "${ROOT}"

# --- Install pinned tools ---------------------------------------------------
GOBIN="$(go env GOPATH)/bin"
echo "==> go: $(go version)"
echo "==> installing pinned dev tools (tools/go.mod directives) into ${GOBIN}"
go -C tools install tool

echo ""
echo "==> done. Installed: buf, protoc-gen-go, protoc-gen-go-grpc,"
echo "    protoc-gen-grpc-gateway, protoc-gen-openapiv2, sqlc, govulncheck, benchstat."
echo "    Ensure ${GOBIN} is on PATH (the Makefile does this automatically)."
echo "    Next: cd ${ROOT} && make gen && make build"

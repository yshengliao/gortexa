#!/usr/bin/env bash
# install.sh — Gortexa dev-environment bootstrap. Homebrew-style: safe to run
# from a checkout (./install.sh) or piped straight from the web:
#
#   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
#
# It corrects the Go module env for this process, then installs the pinned dev
# toolchain declared as go.mod `tool` directives in tools/go.mod (buf + protoc
# plugins + sqlc + govulncheck + benchstat) via `go install tool`. Idempotent:
# re-running just reinstalls the same pinned versions.
#
# Side effects, stated up front:
#   - When run outside a checkout, it clones the repo into ./gortexa (override
#     with GORTEXA_DIR) to read the pinned tool versions; delete it afterwards
#     if you don't want a checkout.
#   - It only writes persistent Go config (~/.config/go/env) when it detects
#     the known-broken dev-container values; on a normal machine your global
#     Go configuration is left untouched.
set -euo pipefail

# --- Preconditions ----------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go is not installed. Install Go >= 1.27 from https://go.dev/dl/ then re-run." >&2
  exit 1
fi
# GOTOOLCHAIN auto-download (which fetches Go 1.27 for this repo) needs Go >= 1.21;
# older toolchains fail later with unrelated flag-parse errors, so gate here.
GO_MINOR="$(go env GOVERSION 2>/dev/null | sed -n 's/^go1\.\([0-9]*\).*/\1/p')"
if [ -z "${GO_MINOR}" ] || [ "${GO_MINOR}" -lt 21 ]; then
  echo "ERROR: your Go ($(go version 2>/dev/null || echo unknown)) is too old to auto-download Go 1.27 via GOTOOLCHAIN." >&2
  echo "       Install Go >= 1.21 from https://go.dev/dl/ then re-run." >&2
  exit 1
fi

# Read the persisted GOPRIVATE BEFORE overriding anything in this process, so
# the broken-container detection below sees the machine's value, not our export.
PERSISTED_GOPRIVATE="$(go env GOPRIVATE 2>/dev/null || true)"

# --- Corrected Go env (this process only) ------------------------------------
# The dev container ships GOPRIVATE/GOPROXY values that force github.com /
# google.golang.org / go.opentelemetry.io to resolve direct-via-git, which the
# network policy 403s. Export corrected values for this process; modules then
# route through proxy.golang.org + sum.golang.org.
export GOPRIVATE="" GOINSECURE="" GONOSUMCHECK="" GONOSUMDB=""
export GOFLAGS="-mod=mod"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

# --- Persist corrections ONLY on machines with the broken container values ---
# `go env -w` writes ~/.config/go/env and outlives this shell; on a normal
# developer machine that would clobber unrelated private-module setups, so it
# is gated on detecting the known-bad pattern.
case "${PERSISTED_GOPRIVATE}" in
  *github.com*|*google.golang.org*|*go.opentelemetry.io*)
    echo "==> detected broken container Go env (GOPRIVATE=${PERSISTED_GOPRIVATE})"
    echo "    persisting corrected module settings to \$(go env GOENV)"
    echo "    restore later with: go env -w GOPRIVATE=\"${PERSISTED_GOPRIVATE}\""
    go env -w GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" GOTOOLCHAIN="${GOTOOLCHAIN}" \
              GOFLAGS="${GOFLAGS}" GOPRIVATE="" GOINSECURE="" 2>/dev/null || true
    ;;
esac

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
  echo "==> no checkout found; cloning ${REPO_URL} (${REPO_REF}) into ./${DEST}"
  echo "    (used to read the pinned tool versions; set GORTEXA_DIR to change the"
  echo "     location, delete the directory afterwards if you don't want a checkout)"
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
echo "    Add them to PATH (now, and in your shell profile):"
echo "      export PATH=\"${GOBIN}:\$PATH\""
echo "    Next (new project):    go install github.com/yshengliao/gortexa/cmd/gortexa@latest"
echo "                           gortexa create myapp --module github.com/me/myapp"
echo "    Next (this checkout):  cd ${ROOT} && make gen && make build"

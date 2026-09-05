#!/usr/bin/env bash
# proto-regen: deterministic proto code generation for Gortexa.
# Order: lint -> breaking -> generate. Stops at first failure.
# Paths are fixed by design (no external path args) for security.
set -euo pipefail

# Resolve repo root: this script lives at .skills/proto-regen/scripts/regen.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

if ! command -v buf >/dev/null 2>&1; then
  echo "ERROR: 'buf' not found in PATH. Run 'make bootstrap' first." >&2
  exit 1
fi

echo "==> [1/3] buf lint"
buf lint

echo "==> [2/3] buf breaking (against git default branch)"
# Determine default branch; fall back to main.
DEFAULT_BRANCH="$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@' || echo main)"
if [ "${BUF_ALLOW_BREAKING:-0}" = "1" ]; then
  echo "    BUF_ALLOW_BREAKING=1 set; skipping breaking-change gate (intended break)."
else
  if git rev-parse --verify "${DEFAULT_BRANCH}" >/dev/null 2>&1; then
    buf breaking --against ".git#branch=${DEFAULT_BRANCH}"
  else
    echo "    No '${DEFAULT_BRANCH}' ref found (fresh repo?); skipping breaking check."
  fi
fi

echo "==> [3/3] buf generate"
if [ -f api/buf.gen.yaml ]; then buf generate --template api/buf.gen.yaml --path proto/gortexa; fi
buf generate --exclude-path proto/gortexa

echo "==> Done. Generated artifacts are under gen/. Do NOT hand-edit them."

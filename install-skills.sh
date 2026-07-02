#!/usr/bin/env bash
# install-skills.sh — wire every .skills/* SSOT skill into all four AI tools'
# skill dirs. Default: symlink (single source of truth, auto-sync).
# --copy: copy instead (for Windows non-WSL where symlinks clone as text).
set -euo pipefail

MODE="symlink"
[ "${1:-}" = "--copy" ] && MODE="copy"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${REPO_ROOT}"

if [ ! -d ".skills" ]; then
  echo "ERROR: .skills/ not found. Run this from the repo root." >&2
  exit 1
fi

# In symlink mode the relative path is computed with python3. Check for it up
# front, before the per-skill loop's `rm -rf` deletes any already-wired skill —
# otherwise a missing python3 would destroy skills mid-run and then fail.
if [ "${MODE}" = "symlink" ] && ! command -v python3 >/dev/null 2>&1; then
  echo "ERROR: python3 is required for symlink mode; re-run with --copy." >&2
  exit 1
fi

# Four tool skill roots. Codex and Antigravity also scan .agents/skills, but each
# gets its documented primary path for clarity.
TOOL_DIRS=(
  ".claude/skills"   # Claude Code
  ".codex/skills"    # OpenAI Codex
  ".github/skills"   # GitHub Copilot
  ".agents/skills"   # Google Antigravity (also Codex fallback)
)

count=0
for src in .skills/*/; do
  [ -d "${src}" ] || continue
  src="${src%/}"
  name="$(basename "${src}")"
  for tool in "${TOOL_DIRS[@]}"; do
    target="${tool}/${name}"
    mkdir -p "${tool}"
    rm -rf "${target}"
    if [ "${MODE}" = "symlink" ]; then
      # relative symlink from the tool dir back to the SSOT skill
      rel="$(python3 -c "import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))" "${src}" "${tool}")"
      ln -s "${rel}" "${target}"
    else
      cp -R "${src}" "${target}"
    fi
  done
  # keep any bundled scripts executable
  if [ -d "${src}/scripts" ]; then
    find "${src}/scripts" -type f -name '*.sh' -exec chmod +x {} + 2>/dev/null || true
  fi
  count=$((count + 1))
  echo "  wired ${name}"
done

echo ""
echo "Done (${MODE} mode). ${count} skill(s) wired into Claude Code, Codex, Copilot, Antigravity."
echo "Restart each tool's session so it re-scans its skills directory."

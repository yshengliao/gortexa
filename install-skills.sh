#!/usr/bin/env bash
# install-skills.sh — wire the .skills/ SSOT into all four AI tools' skill dirs.
# Default: symlink (single source of truth, auto-sync).
# --copy: copy instead (for Windows non-WSL where symlinks clone as text).
set -euo pipefail

MODE="symlink"
[ "${1:-}" = "--copy" ] && MODE="copy"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${REPO_ROOT}"

SRC=".skills/proto-regen"
if [ ! -d "${SRC}" ]; then
  echo "ERROR: ${SRC} not found. Run this from the repo root after .skills/ exists." >&2
  exit 1
fi

# Four tool dirs. Codex and Antigravity both also scan .agents/skills,
# but we give each its documented primary path for clarity.
TARGETS=(
  ".claude/skills/proto-regen"     # Claude Code
  ".codex/skills/proto-regen"      # OpenAI Codex
  ".github/skills/proto-regen"     # GitHub Copilot
  ".agents/skills/proto-regen"     # Google Antigravity (also Codex fallback)
)

for t in "${TARGETS[@]}"; do
  mkdir -p "$(dirname "${t}")"
  rm -rf "${t}"
  if [ "${MODE}" = "symlink" ]; then
    # relative symlink from target dir back to SRC
    rel="$(python3 -c "import os; print(os.path.relpath('${SRC}', os.path.dirname('${t}')))")"
    ln -s "${rel}" "${t}"
    echo "  symlinked ${t} -> ${rel}"
  else
    cp -R "${SRC}" "${t}"
    echo "  copied    ${SRC} -> ${t}"
  fi
done

# Ensure the generation script stays executable.
chmod +x "${SRC}/scripts/regen.sh"

echo ""
echo "Done (${MODE} mode). proto-regen skill wired into Claude Code, Codex, Copilot, Antigravity."
echo "Restart each tool's session so it re-scans its skills directory."

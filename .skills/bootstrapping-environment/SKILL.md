---
name: bootstrapping-environment
description: Install and verify the Gortexa dev toolchain — buf, protoc-gen-go(-grpc), grpc-gateway plugins, sqlc, govulncheck — pinned via go.mod tool directives, using `gortexa tools install` and `gortexa doctor` (or install.sh / make bootstrap). Use when buf or a proto plugin is missing, code generation fails on a missing tool, or setting up a fresh checkout.
---

# bootstrapping-environment

Install the pinned developer toolchain for a Gortexa project. Tools are declared
as `tool` directives in `tools/go.mod` and installed with `go install tool`, so
versions are reproducible across machines.

## When to use this skill
- `buf` or a `protoc-gen-*` plugin is "not found in PATH".
- `gortexa gen` / `make gen` fails because a tool is missing.
- Setting up a freshly cloned project.

## Procedure

1. **Install the pinned tools.**
   ```bash
   gortexa tools install      # == go -C tools install tool
   ```
   Or, from outside a checkout, the one-liner installer:
   ```bash
   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
   ```
   Or `make bootstrap`.

2. **Verify.**
   ```bash
   gortexa doctor
   ```
   Confirms Go ≥ 1.27 and that buf + the proto plugins + sqlc are on PATH. Ensure
   `$(go env GOPATH)/bin` is on PATH (the Makefile adds it automatically).

3. **Upgrade or re-pin a tool** when needed (full module path required — bare
   tool names are rejected by `go get -tool`):
   ```bash
   gortexa tools sync github.com/bufbuild/buf/cmd/buf@latest
   ```

## Hard rules
- The dev container ships a GOPRIVATE/GOPROXY that 403s; always build through
  `make` (it exports the corrected env) or run `install.sh` first.
- Do not add tools via ad-hoc `go install pkg@latest`; add a pinned `tool`
  directive (`gortexa tools sync pkg@version`) so installs stay reproducible.

## Related
- `scaffolding-projects` and `generating-apis` — use the tools to build APIs.

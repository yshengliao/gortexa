---
name: scaffolding-projects
description: Create a new batteries-included Gortexa project from the framework layout with `gortexa create` — one h2c port serving gRPC, HTTP/JSON (grpc-gateway) and MCP, with the interceptor chain, error model, config, health and a sample resource. Use when starting a new Gortexa service or project from scratch.
---

# scaffolding-projects

Bootstrap a new Gortexa project. `gortexa create` clones the framework layout,
drops its git history, and rewrites the Go module path, leaving a working project
(gRPC + HTTP/JSON + MCP on one port) with a sample resource to learn from.

## When to use this skill
- The user wants to start a new Gortexa-based service/project from scratch.
- A fresh contract-first gRPC project with batteries (config, health,
  interceptor chain, MCP) is needed.

## Procedure

1. **Create the project.**
   ```bash
   gortexa create <path> --module <go-module-path>
   ```
   e.g. `gortexa create myapp --module github.com/acme/myapp`. Without `--module`
   it defaults to `github.com/example/<name>`.

2. **Bootstrap tools and generate.**
   ```bash
   cd <path>
   make bootstrap     # install pinned tools (buf, protoc plugins, sqlc, …)
   make gen           # buf lint → breaking → generate
   ```

3. **Build and run.**
   ```bash
   make build && make run     # server on :8080
   ```
   Smoke-test:
   ```bash
   curl localhost:8080/healthz
   curl -XPOST localhost:8080/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
   ```

4. **Add your own APIs** with the generating-apis skill (`gortexa gen`).

## Hard rules
- Never hand-edit `gen/` — it is gitignored and produced by `make gen`.
- The sample `resource` service is a reference; once you have your own domains,
  remove its proto/logic and the `gortexa:*`-wired lines in `cmd/server/main.go`.

## Related
- `generating-apis` — add a resource/service to the new project.
- `bootstrapping-environment` — install and verify the dev toolchain.

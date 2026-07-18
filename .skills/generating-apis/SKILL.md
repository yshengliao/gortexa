---
name: generating-apis
description: Add a new API (resource/service) to a Gortexa project end-to-end — a proto contract with validation, AI-tool and HTTP annotations, an in-memory logic stub, and server wiring — using the `gortexa gen` CLI, then regenerate code. Use when asked to add an API, resource, endpoint, CRUD service, RPC, or new domain to a Gortexa project.
---

# generating-apis

Add a new gRPC + HTTP/JSON + MCP API to a Gortexa project in one command.
`gortexa gen` writes the proto contract, an in-memory logic stub, wires it into
`cmd/server/main.go`, and runs the buf pipeline. Protobuf is the single source of
truth; never hand-edit `gen/`.

## When to use this skill
- The user asks to "add an API / resource / endpoint / service / RPC", or to
  scaffold CRUD for a new domain (e.g. orders, invoices, users).
- A new bounded context needs a proto package plus a handler stub wired end-to-end.

## Prerequisites
- A Gortexa project (a `go.mod` with the framework layout) with the dev tools
  installed (`gortexa tools install` or `make bootstrap`). If `buf` is missing,
  run the bootstrapping-environment skill first.

## Procedure

1. **Pick the target.** Choose `<domain>/<version>` (lowercase domain, `vN`
   version) and a CamelCase `Entity`, e.g. `billing/v1 Invoice`. Entity names
   must be unique across domains: the logic stub lands at
   `internal/logic/<entity>.go` without a domain prefix, so a second domain
   reusing the same Entity collides with the first (do not `--force` through
   that collision — it overwrites the other domain's logic).

2. **Generate.** From the project root:
   ```bash
   gortexa gen <domain>/<version> <Entity>
   ```
   This writes `proto/<domain>/<version>/<entity>.proto` and
   `internal/logic/<entity>.go`, wires `cmd/server/main.go` at the `// gortexa:*`
   markers, then runs `buf lint → breaking → generate`. Flags: `--no-wire`
   (skip main.go), `--skip-gen` (skip buf), `--force` (overwrite).

3. **Verify.**
   ```bash
   go build ./... && go test ./...
   ```

4. **Customize.** Edit the proto fields/RPCs to the real shape, then regenerate
   with `gortexa regen`. Back the logic stub in `internal/logic/<entity>.go` with
   `storage` for a real datastore.

## Hard rules
- Never hand-edit files under `gen/`; they are produced only by `buf generate`
  (`gortexa regen`). Edit `proto/`, then regenerate.
- Type alignment: proto `int64` ↔ PostgreSQL `bigint`, `string` ↔ `text`; JSON
  serializes `int64` as a string. Keep proto field types aligned with backing
  columns at design time.
- If a contract change is breaking and intended, run `gortexa regen
  --allow-breaking`; otherwise keep it backward-compatible.

## Related
- `proto-regen` — regenerate code after editing proto by hand.
- `scaffolding-projects` — create a brand-new Gortexa project.

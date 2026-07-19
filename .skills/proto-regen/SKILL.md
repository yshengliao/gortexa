---
name: proto-regen
description: Regenerate gRPC stubs, grpc-gateway HTTP handlers, OpenAPI spec, and MCP-facing code from .proto files in this Gortexa project. Use whenever proto files change, or when asked to regenerate / regen / rebuild gRPC code, update stubs, add an RPC, or change the API contract. Runs buf lint and buf breaking checks before generating so broken proto never pollutes gen/.
---

# proto-regen

Regenerate all code artifacts from Protobuf definitions in this Gortexa project. Protobuf is the single source of truth (SSOT); never hand-edit anything under `gen/`.

## When to use this skill
- A `.proto` file under `proto/` (including `proto/gortexa/ai/v1/`) was added or changed.
- The user asks to "regen", "regenerate", "rebuild stubs", "update the gRPC code", "add an RPC", or otherwise change the API contract.
- After editing `google.api.http` annotations (HTTP/JSON mapping) or `gortexa.ai.v1` AI-skill annotations.

## What this skill does NOT do
- It does not hand-write or hand-edit any file under `gen/`. Generated code is produced only by `buf generate`.
- It does not bypass the lint/breaking checks. Broken proto must not reach `gen/`.

## Procedure

1. **Confirm what changed.** Identify which `.proto` files were edited. If adding an RPC, ensure it has a `google.api.http` annotation when HTTP/JSON access is wanted (see `references/annotation-guide.md`).

2. **Run the generation script** (it enforces the correct order):
   ```bash
   bash .skills/proto-regen/scripts/regen.sh
   ```
   The script runs, in order: `buf lint` → `buf breaking` (against the git default branch) → `buf generate`. It stops at the first failure.

3. **Handle failures:**
   - `buf lint` fails → fix the proto style/structure issue reported, do not touch `gen/`.
   - `buf breaking` fails → the change breaks the contract. Confirm with the user whether the break is intended. If intended, re-run with `BUF_ALLOW_BREAKING=1 bash .skills/proto-regen/scripts/regen.sh`. If not, revise the proto to be backward-compatible.
   - `buf generate` fails → report the plugin error; likely a missing tool (run `make bootstrap`) or a malformed annotation.

4. **Verify output.** Confirm `gen/` now contains updated `*.pb.go`, `*_grpc.pb.go`, `*.pb.gw.go`, and `apidocs.swagger.json` as expected. Do not edit them. The MCP bridge reflects over the generated descriptors at runtime, so no separate MCP codegen step is needed.

5. **Next steps for the user.** Generated code is regenerated; business logic in `internal/` is untouched. Remind that `internal/` handlers may need updating to satisfy new/changed service interfaces, and that this should follow the project's TDD loop (write/adjust the failing test first).

## Hard rules
- Never edit files under `gen/`. They are regenerated, not authored.
- Never edit files under `.skills/proto-regen/scripts/` to take arbitrary paths; the script's paths are fixed by design (security).
- Type alignment: proto `int64` ↔ PostgreSQL `bigint`, `string` ↔ `text`. JSON serialises `int64` as a string. Keep proto field types aligned with the backing column types at design time.

## Reference
- `references/annotation-guide.md` — quick reference for `google.api.http` (REST mapping) and `gortexa.ai.v1` (AI-skill) annotations. Load it when editing or adding annotations.

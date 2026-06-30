# Gortexa — agent rules (always-on)

Gortexa is a contract-first, batteries-included gRPC framework. Protobuf is the
single source of truth; one h2c port multiplexes gRPC + HTTP/JSON (grpc-gateway)
+ MCP. Target **Go 1.26**, buf v2.

## Iron rules
- **Never hand-edit anything under `gen/`.** It is produced only by `make gen`
  (`buf lint → buf breaking → buf generate`). `gen/` is gitignored; regenerate
  after cloning. Editing the contract means editing `proto/` then regenerating.
- **The proto SSOT lives in `proto/`** (`proto/resource/v1`, `proto/ai/v1`).
  Use the `proto-regen` skill (`.skills/proto-regen/`) when changing it.
- **Type alignment:** proto `int64` ↔ PostgreSQL `bigint`, `string` ↔ `text`;
  JSON serializes `int64` as a string. Keep proto field types aligned with
  backing columns at design time.
- **Environment:** the container ships a broken `GOPRIVATE`/`GOPROXY` that 403s.
  Always build through `make` (it exports the corrected env) or run `install.sh`
  first. Never `go get`/clone the upstream `gortex` repo — it is out of scope and
  unreachable; the cross-cutting modules here are clean-room implementations.
- **Errors never leak internals.** Return `*errors.Error` with a `Category`;
  the three transports map it via `internal/errors`. Internal errors expose only
  a SafeMessage.
- **OTel is a StatsHandler, not an interceptor.** Do not add the deprecated
  otelgrpc interceptors.
- **Tests:** TDD where practical. Use `synctest.Test` (not `synctest.Run`) for
  time-dependent logic, `goleak` for goroutine hygiene, golden files for schema
  output. Docker-dependent integration tests live behind `//go:build integration`.

## Common commands
- `make bootstrap` — install the pinned dev toolchain (`tools/go.mod` `tool`
  directives: buf + protoc plugins + sqlc + …) via `install.sh`.
- `make gen` — regenerate from proto.
- `make build` / `make test` / `make cover` / `make lint`.
- `make run` — start the sample server.

## Developer CLI & skills
- `gortexa` (`cmd/gortexa`) — `create` a project, `gen` an API (proto + logic +
  wiring + codegen), `regen`, `run`, `tools install`, `skills install`, `doctor`.
- `.skills/*` — AI-assist skills (proto-regen, generating-apis,
  scaffolding-projects, bootstrapping-environment) wired into Claude/Codex/
  Copilot/Antigravity by `install-skills.sh`. Invoke the matching skill for a task.

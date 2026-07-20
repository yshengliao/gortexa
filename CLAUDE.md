# Gortexa — agent rules (always-on)

Gortexa is a contract-first, batteries-included gRPC framework. Protobuf is the
single source of truth; one h2c port multiplexes gRPC + HTTP/JSON (grpc-gateway)
+ MCP. Target **Go 1.26**, buf v2.

## Iron rules
- **Never hand-edit anything under `gen/`.** It is produced only by `make gen`
  (`buf lint → buf breaking → buf generate`). `gen/` is committed (module
  consumers need it: the gortexa.ai.v1 bindings are imported by mcp, and consumer
  `go mod tidy` resolves the gen/resource test dependencies of imported
  packages) — but it is never hand-edited; `make gen` regenerates it
  byte-identically and CI fails on drift. Editing the contract means editing
  `proto/` then regenerating.
- **The proto SSOT lives in `proto/`** (`proto/resource/v1`, `proto/gortexa/ai/v1`).
  Use the `proto-regen` skill (`.skills/proto-regen/`) when changing it.
- **Type alignment:** proto `int64` ↔ PostgreSQL `bigint`, `string` ↔ `text`;
  JSON serializes `int64` as a string. Keep proto field types aligned with
  backing columns at design time.
- **Environment:** the container ships a broken `GOPRIVATE`/`GOPROXY` that 403s.
  Always build through `make` (it exports the corrected env) or run
  `make bootstrap` first (in the framework repo it delegates to `install.sh`;
  scaffolded projects have no install.sh and install the pinned tools from
  `tools/go.mod`). Never `go get`/clone the upstream `gortex` repo — it is out
  of scope and unreachable; the cross-cutting modules here are clean-room
  implementations.
- **Errors never leak internals.** Return `*apperr.Error` with a `Category`;
  the three transports map it via `apperr`. Only `InvalidArgument` and
  `Unauthenticated` forward the caller-provided message; every other category
  (including Internal) exposes only its registry SafeMessage, and a wrapped
  cause is never serialized.
- **OTel is a StatsHandler, not an interceptor.** Do not add the deprecated
  otelgrpc interceptors.
- **One h2c port stays one port.** The main listener multiplexes gRPC +
  HTTP/JSON + MCP on a single h2c port — that is not negotiable. A separate
  ops/admin surface is opt-in via `kernel.WithAdminListener(addr)` (health only)
  or `WithExtraListener(lis, h)`; it never changes the main port's multiplexing.
- **Auth is pluggable; the chain order is not.** The fixed eight-stage chain is
  immutable, but its auth stage runs an `auth.Authenticator` — JWT
  (`NewJWTAuthenticator`) is just the default. Set `interceptor.Config.Verifier`
  for JWT (unchanged) or `Config.Authenticator` for any other scheme (static
  bearer, mTLS, API key); Authenticator wins when both are set. A consumer adds
  its own interceptors via `kernel.WithServerOptions(...)` (gRPC chains them
  after the stock chain, inside recover), never by editing the eight stages.
- **Tests:** TDD where practical. Use `synctest.Test` (not `synctest.Run`) for
  time-dependent logic, `goleak` for goroutine hygiene, golden files for schema
  output. Docker-dependent integration tests live behind `//go:build integration`.

## Common commands
- `make bootstrap` — install the pinned dev toolchain (`tools/go.mod` `tool`
  directives: buf + protoc plugins + sqlc + …).
- `make gen` — regenerate from proto.
- `make build` / `make test` / `make cover` / `make lint`.
- `make run` — start the sample server.

## Developer CLI & skills
- `gortexa` — the installed CLI (source: `cmd/gortexa` in the framework repo;
  pruned from scaffolds) — `create` a project, `gen` an API (proto + logic +
  wiring + codegen), `regen`, `run`, `tools install`, `skills install`, `doctor`.
- `.skills/*` — AI-assist skills (proto-regen, generating-apis,
  scaffolding-projects, bootstrapping-environment) wired into Claude/Codex/
  Copilot/Antigravity by `install-skills.sh`. Invoke the matching skill for a task.

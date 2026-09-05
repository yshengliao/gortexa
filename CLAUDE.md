# Gortexa — agent rules (always-on)

Gortexa is a contract-first, batteries-included gRPC framework. Protobuf is the
single source of truth; one h2c port multiplexes gRPC + HTTP/JSON (grpc-gateway)
+ MCP. Target **Go 1.27**, buf v2.

## Iron rules
- **Never hand-edit anything under `gen/` or `api/gen/`.** They are produced only
  by `make gen` (`buf lint → buf breaking → buf generate`). Both are committed
  (module consumers need them: the gortexa.ai.v1 bindings are imported by mcp, and
  consumer `go mod tidy` resolves the gen/resource test dependencies of imported
  packages) — but never hand-edited; `make gen` regenerates them byte-identically
  and CI fails on drift. Editing the contract means editing `proto/` then
  regenerating.
- **The proto SSOT lives in `proto/`** (`proto/resource/v1`, `proto/gortexa/ai/v1`).
  Use the `proto-regen` skill (`.skills/proto-regen/`) when changing it.
- **`proto/gortexa/ai/v1` generates into the `api/` module, not `gen/`.** It is a
  separate Go module (`github.com/yshengliao/gortexa/api`) with its own
  `buf.gen.yaml`, and the root template excludes that path, because protobuf's
  global registry is keyed on the proto file path: a scaffolded project consumes
  this module instead of regenerating the descriptor, and a second copy panics at
  init. Do not fold it back into `gen/`.
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

## Releasing

`api/` is a nested module, so it carries its own tag (`api/vX.Y.Z`) and that tag
can only be cut from a commit that already contains `api/`. `replace` is ignored
by anything depending on gortexa, so a root `go.mod` requiring an untagged api
version is unbuildable for every consumer and for every `gortexa create` project
(which drops the replace but keeps the require). Tag in this order, in one
sitting — main is broken for consumers in between:

1. `git tag api/vX.Y.Z && git push origin api/vX.Y.Z`
2. `go mod edit -require=github.com/yshengliao/gortexa/api@vX.Y.Z && go mod tidy`,
   commit and push. Keep the `replace` — local development still needs it.
3. Only then tag the framework: `git tag vX.Y.Z && git push origin vX.Y.Z`.

The `replace` also means the framework always compiles against the local
`./api`, while consumers get the tag. **Any change under `api/`** (a
`proto/gortexa/ai/v1` edit regenerates it) therefore needs a new `api/vX.Y.Z`
and a root `require` bump in the same PR. CI's "Build without the api replace"
step builds the tree the way a consumer resolves it, so that mismatch fails
there instead of shipping.

One-time for v0.28: once v0.28.0 is the `buf breaking` comparison base, remove
the `FILE_SAME_GO_PACKAGE` entry from `buf.yaml`'s `breaking.except` — it exists
only to let the annotations `go_package` move land.

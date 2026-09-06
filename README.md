# Gortexa

[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docs](https://img.shields.io/badge/docs-gortexa.sheng.page-2563EB)](https://gortexa.sheng.page)
[![Status](https://img.shields.io/badge/status-active-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![AI generated](https://img.shields.io/badge/AI%20generated-Fable%205.1%20%7C%20Opus%204.8%20%7C%20Gemini%203.1%20Pro%20%7C%20Codex%205.5-8A2BE2)](#provenance)

A contract-first, batteries-included **gRPC framework** for Go 1.27. Protobuf is
the single source of truth; **one h2c port** multiplexes three protocols:

- **gRPC** (native, over cleartext HTTP/2)
- **HTTP/JSON** via grpc-gateway (`google.api.http` annotations)
- **MCP** (Model Context Protocol, Streamable HTTP) for AI agents

…all sharing one interceptor chain, one error model, and one auth path.

**Documentation: [gortexa.sheng.page](https://gortexa.sheng.page)** — quickstarts, concepts, configuration, and per-component usage after `go get` ([Components](https://gortexa.sheng.page/components/)).

## Highlights

- **Single-port multiplexing** — Content-Type / path dispatch on native h2c
  (`http.Server.Protocols`, no deprecated `x/net/h2c`). External gRPC rides
  grpc-go's `ServeHTTP` handler mode (Go's HTTP/2 stack), which upstream marks
  Experimental — a few transport-level features differ from a dedicated gRPC
  port. Split gRPC onto its own listener (e.g. cmux) if you need grpc-go's full
  transport semantics on the external surface.
- **Fixed-order interceptor chain** (recover → request-id → logger → load-shed →
  rate-limit → circuit-breaker → auth → validation), fail-loud if incomplete.
  OTel is a `StatsHandler`, covering unary **and** streaming. The order is fixed,
  but the auth stage is pluggable: set `interceptor.Config.Verifier` for the
  built-in HS256 JWT, or `Config.Authenticator` (an `auth.Authenticator`) for
  static bearer / mTLS / API-key. Add your own interceptors with
  `kernel.WithServerOptions(...)`.
- **One error model, three transports** — a single mapping table drives gRPC
  status, HTTP status, and MCP error envelopes. Only invalid-argument and
  unauthenticated errors forward their message; everything else surfaces a
  registry-safe message, and internal causes never leak.
- **AI-skills layer** — `gortexa.ai.v1` annotations → provider-neutral IR →
  MCP / OpenAI-strict / Gemini tool schemas (golden-locked). The MCP bridge
  dispatches `tools/call` back through the **full interceptor chain** via an
  in-process loopback, so AI calls inherit auth/validation.
- **Batteries** — config (layered, fail-loud, masked secrets),
  3-state health → gRPC Health, slog + OTLP, a PgBouncer-safe pgx pool + sqlc,
  and pluggable cache / MQ abstractions. The cache defaults to a process-local
  in-memory backend (no external service; `cache.driver: redis` opts in to a
  distributed cache, served by a small in-tree zero-dependency RESP client);
  MQ is NATS — core (default, at-most-once) or JetStream
  (`mq.driver: jetstream`; durable, at-least-once with redelivery on handler
  error, so handlers must be idempotent). Delivery semantics are uniform:
  `mq.group_id` empty (default) fans out, non-empty load-balances (queue
  group / durable consumer); `mq.url` accepts a comma-separated server list.

## Quickstart

Install the dev toolchain — a Homebrew-style one-liner that installs the pinned tools (buf, protoc plugins, sqlc, …). Run outside a checkout it clones this repo into `./gortexa` (override with `GORTEXA_DIR`, delete afterwards if unwanted); it only persists Go env corrections when it detects the broken dev-container values:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
```

Install the CLI:

```bash
go install github.com/yshengliao/gortexa/cmd/gortexa@latest
export PATH="$(go env GOPATH)/bin:$PATH"   # go install lands here; add to your shell profile
```

Without the PATH line the next command fails with `command not found: gortexa` —
most shells do not have `$(go env GOPATH)/bin` on PATH by default.

Scaffold a project, generate a CRUD API, and run it on one h2c port (`:8080`):

```bash
gortexa create myapp --module github.com/me/myapp
cd myapp
gortexa gen billing/v1 Invoice
gortexa run
```

Or work in this repo directly with `make` (`bootstrap` installs the pinned toolchain; `gen` runs buf lint → breaking → generate; `test` runs race tests with in-process fakes):

```bash
make bootstrap
make gen
make test
make run
```

Then, against the running server (health is open; `/v1/resources/x` returns 401 — auth is shared with gRPC):

```bash
curl localhost:8080/healthz
curl -XPOST localhost:8080/mcp -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
curl localhost:8080/v1/resources/x
```

## Use as a module

The scaffold above gives you the full experience — proto pipeline, sample
service, one-command codegen. But every framework package is also importable
directly:

```bash
go get github.com/yshengliao/gortexa@latest
```

A minimal app — one h2c port with gRPC health, `/healthz`, `/readyz` — needs no
code generation at all (unlike the scaffold's sample server above, it wires no
gateway and no MCP bridge, so only the health endpoints respond until you add
them). `kernel.New` requires an explicit decision about the governance chain:
pass `WithInterceptors` to install the eight stages, or `WithoutInterceptors`
to serve without them. This example takes the second, which is only safe
because it serves nothing but health on a port you have not exposed — add
`WithInterceptors` before you register a real service:

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/yshengliao/gortexa/health"
	"github.com/yshengliao/gortexa/kernel"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// defaults: h2c on :8080, graceful shutdown. WithoutInterceptors is a
	// deliberate opt-out: no recover, auth, rate limit or validation.
	app, err := kernel.New(kernel.WithoutInterceptors())
	if err != nil {
		log.Fatal(err)
	}
	// Register your own generated gRPC services on app.GRPCServer().
	app.Health().Register("self", func(context.Context) health.State { return health.Healthy })

	if err := app.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
```

From there, add what you need — both the AI and the non-AI path are first-class:

- **Non-AI service**: `config.MustBuild` for layered config, `interceptor.NewSet`
  for the governed chain, `httpcompat` for the grpc-gateway mux, `apperr` for
  the three-transport error model, and `mq` / `cache` / `storage` / `client`
  as batteries. See `cmd/server/main.go` for the full wiring, and
  [gortexa.sheng.page/components](https://gortexa.sheng.page/components/) for
  each package's purpose, import path and a minimal example.
- **AI service**: annotate your protos with the gortexa.ai.v1 annotations (their Go
  bindings ship in the companion `github.com/yshengliao/gortexa/api` module) and
  wire `mcp.NewBridge` to expose the annotated RPCs as MCP tools behind the same
  interceptor chain.

Notes:

- A project created by `gortexa create` contains the framework source under its
  own module path, but it no longer conflicts with the published one. The
  gortexa.ai.v1 annotations are not copied: they come from the small
  `github.com/yshengliao/gortexa/api` module that both the project and the
  framework depend on, so the descriptor is registered exactly once. The sample
  service is moved into the project's own proto namespace (`<project>.resource.v1`
  at `proto/<project>/resource/v1`) rather than the layout's `resource.v1`, so two
  projects — or a project and the framework's own sample — can be linked into one
  binary. Both used to panic at init: protobuf's global registry is keyed on the
  proto file path and package, neither of which the module rewrite changes.
- Depending on the framework is therefore safe, but remember what the project
  already has: it carries its own copy of the framework packages, so importing
  `github.com/yshengliao/gortexa/...` as well gives you two independent copies of
  that code. Reach for it when you want something your copy does not have.
- Pair versions: a `gortexa` CLI ≥ v0.28 generates code for projects scaffolded
  from ≥ v0.28 (and vice versa). The framework package layout changed in v0.27,
  and v0.28 moved the annotations bindings into the api module and namespaced the
  sample. A project records what it was scaffolded from in
  `.gortexa/project.json`; `gortexa doctor` prints it, and `gortexa gen` reads the
  proto namespace from it — a project without that file predates v0.28 and keeps
  the flat proto layout its existing services already use.

## Developer CLI

`gortexa` (in `cmd/gortexa`) makes the framework batteries-included:

| Command | What |
|---|---|
| `gortexa create <path>` | Scaffold a new project (clone the layout, rewrite the module path). |
| `gortexa gen <domain>/<v> [Entity]` | Generate a CRUD API end-to-end: proto + logic stub + server wiring, then regenerate. |
| `gortexa regen` | Regenerate from proto (buf lint → breaking → generate). |
| `gortexa run` | Build and run the dev server. |
| `gortexa export --format=mcp\|openai\|gemini` | Export the project's `gortexa.ai.v1` tool schemas as provider-ready JSON. |
| `gortexa tools install` / `sync` | Install / re-pin the dev toolchain (`tools/go.mod` directives). |
| `gortexa skills install` / `list` | Wire the AI-assist skills into Claude/Codex/Copilot/Antigravity. |
| `gortexa doctor` | Check the Go toolchain and proto tools. |

## Layout

| Path | What |
|---|---|
| `proto/` | Proto SSOT (`resource/v1`, `gortexa/ai/v1`). Edit here, then `make gen`. In a scaffolded project the sample lives under the project's namespace instead. |
| `gen/` | Generated code — **never hand-edit**, produced by `make gen`. Committed so the published module is self-contained for consumers; CI guards drift. |
| `api/` | The `github.com/yshengliao/gortexa/api` module: the generated gortexa.ai.v1 bindings, and nothing else. Separate so a project and the framework share one copy of the annotations descriptor instead of each registering their own. `gortexa create` prunes it and depends on it. |
| `apperr/ auth/ cache/ client/ config/ health/ httpcompat/ interceptor/ kernel/ mcp/ mq/ observability/ storage/ testutil/` | Importable framework packages (`go get github.com/yshengliao/gortexa`). |
| `internal/` | Non-API internals: `logic` (sample business logic, `gortexa gen` writes here), `resp` (RESP client backing the redis cache), `storage/db` (sqlc output). |
| `cmd/server/` | Sample server wiring everything onto one port. |
| `cmd/gortexa/` | The `gortexa` developer CLI (create / gen / regen / run / tools / skills / doctor). |
| `tools/` | Pinned dev toolchain as go.mod `tool` directives (buf, protoc plugins, sqlc, …). |
| `.skills/` | Cross-tool AI skills (proto-regen, generating-apis, scaffolding-projects, …) for Claude/Codex/Copilot/Antigravity. |

## Notes

- Requires Go 1.27 (auto-downloaded via `GOTOOLCHAIN`, which itself needs an
  installed Go >= 1.21). `make` exports the corrected module proxy env; run
  `install.sh` once if building outside `make`.
- Integration tests needing real NATS/Redis/Postgres+PgBouncer are behind
  the `integration` build tag (`make test-integration`); the default suite
  needs no services.

## Deploying

- **Terminate TLS in front of it.** The port is cleartext h2c and the server
  does no TLS itself — bearer tokens transit it — so put it behind a reverse
  proxy, service mesh, or load balancer that terminates TLS before exposing it.
- **Authorization is the app's job.** The framework authenticates (verifies the
  JWT) and exposes the token's `Roles` via `auth.ClaimsFrom(ctx)`, but enforces
  no role/permission policy — add your own checks in your handlers.
- Register real dependency checks with `app.Health().Register(...)` so `/readyz`
  reflects DB/cache/MQ reachability; the sample only registers a static `self`.
- **Probes can target a private port.** `kernel.WithAdminListener(addr)` serves
  `/healthz` and `/readyz` on a separate plain-HTTP port, so an orchestrator can
  probe health without reaching the public h2c port; `WithExtraListener(lis, h)`
  serves any handler (pprof, custom metrics) on a caller-owned listener. Both
  are opt-in; the main single-port multiplexing is unchanged.
- **Per-peer rate limiting keys on the direct peer IP.** Behind a TLS-terminating
  proxy or load balancer, all clients share the proxy's IP — and therefore one
  200 RPS bucket — and one abusive client can exhaust it for everyone. Tune or
  disable the rate limiter (`cmd/server/main.go`) in that topology, or enforce
  limits at the proxy where the real client IP is visible.

## Performance

Freshly measured on an **Apple M1** (`go1.26.5`, `darwin/arm64`) with
`go test -benchmem -count=8` summarized by `benchstat` — reproduce with
**`make bench`**. `allocs/op` and `B/op` are the machine-independent signals
(they match earlier CI-host runs exactly); `ns/op` is host-specific (an M1 runs
faster than the shared CI Xeon these paths were previously reported on).

**Framework hot paths** (Apple M1, `go1.26.5`, benchstat median of 8):

| Hot path | benchmark | ns/op | B/op | allocs/op |
|---|---|--:|--:|--:|
| Error resolve → gRPC status (3-transport map) | `ErrorResolve` | ~70 | 104 | 2 |
| Rate-limiter `Allow` (sharded, serial) | `RateLimiterAllow` | ~130 | 0 | 0 |
| Rate-limiter `Allow` (parallel) | `RateLimiterAllowParallel` | ~38 | 0 | 0 |
| MCP tool downgrade (per tool) | `DowngradeMCP` | ~10 | 2 | 1 |
| MCP `tools/list` (memoized) | `ToolsListMemoized` | ~142 | 360 | 3 |
| Resource clone (proto deep-copy) | `ResourceClone` | ~121 | 176 | 2 |
| Resource get (in-memory store) | `GetResource` | ~131 | 176 | 2 |
| Interceptor chain, auth skipped (health/reflection path) | `ChainUnaryAuthSkipped` | ~1,552 | 1,969 | 27 |
| Interceptor chain, authenticated (8 stages, unary) | `ChainUnaryJWT` | see below | 5,680 | 80 |

The paths that must never allocate (rate-limiter `Allow`) hold at **0 allocs/op**.

A correction worth stating plainly: the `~1,552 ns / 27 allocs` row was
previously labelled "full interceptor chain (8 stages)", but the benchmark
behind it sets `AuthSkip` to always return true, so the auth stage returns
before any HS256 verification, metadata lookup or JWT parse. It measures the
health and reflection path, not a normal request. The authenticated row is the
one a capacity plan needs: **80 allocs/op**, about 2.9x the figure this table
used to publish as the eight-stage cost. Both benchmarks now ship, named for
what they measure.

The `B/op` and `allocs/op` columns are the machine-independent signals and
reproduce the earlier CI-host numbers for every row except the chain ones,
which were re-measured when the authenticated variant was added.

**The `errors.AsType` win (historical, same-toolchain A/B).** The three-transport
error resolver swapped the reflection-based `errors.As` for Go 1.26's generic
`errors.AsType[*Error]`, removing one allocation per resolve. The `before` row
needs the pre-swap code, so it is kept as the original measurement (`go1.26.4`,
`BenchmarkErrorResolve`, n=8, on a 2.8 GHz host); the durable result is the
**allocation** reduction, which the M1 run above still confirms at **104 B / 2
allocs**:

| `resolve` via | ns/op | B/op | allocs/op |
|---|--:|--:|--:|
| `errors.As` (before) | 242.3 | 112 | 3 |
| `errors.AsType` (after) | 144.8 | 104 | **2** |
| **Δ** | **−40%** | **−7%** | **−33%** (p=0.000) |

`BenchmarkBridgeHandlePost` separately drives a **512 KB** MCP request through the
full HTTP → JSON-RPC → dispatch path: on the M1 it runs in ~3.53 ms at 1.55 MiB /
115 allocs, where the read/parse of the large body dominates — it measures
end-to-end large-body handling rather than any single hot path.

## Provenance

Gortexa was built with AI-assisted development and hardened through **four
independent model review rounds** — correctness, concurrency, security, and
protocol conformance — with every actionable finding fixed and verified
(`make build / vet / test -race / lint`):

| Model | Role |
|---|---|
| **Claude Fable 5.1** (succeeding Fable 5) | Fourth full-codebase review, hardening fixes and comprehensive test coverage; the v0.28 api-module split review, its release process, and the bilingual docs pipeline |
| **Claude Opus 4.8** | Design, implementation, and consolidation |
| **Gemini 3.1 Pro** (Jules) | Second independent review |
| **Codex 5.5** | Third independent review |

## License

Released under the [MIT License](LICENSE).

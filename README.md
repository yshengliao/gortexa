# Gortexa

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Status](https://img.shields.io/badge/status-active-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![AI generated](https://img.shields.io/badge/AI%20generated-Fable%205%20%7C%20Opus%204.8%20%7C%20Gemini%203.1%20Pro%20%7C%20Codex%205.5-8A2BE2)](#provenance)

A contract-first, batteries-included **gRPC framework** for Go 1.26. Protobuf is
the single source of truth; **one h2c port** multiplexes three protocols:

- **gRPC** (native, over cleartext HTTP/2)
- **HTTP/JSON** via grpc-gateway (`google.api.http` annotations)
- **MCP** (Model Context Protocol, Streamable HTTP) for AI agents

…all sharing one interceptor chain, one error model, and one auth path.

## Highlights

- **Single-port multiplexing** — Content-Type / path dispatch on native h2c
  (`http.Server.Protocols`, no deprecated `x/net/h2c`).
- **Fixed-order interceptor chain** (recover → request-id → logger → load-shed →
  rate-limit → circuit-breaker → auth → validation), fail-loud if incomplete.
  OTel is a `StatsHandler`, covering unary **and** streaming.
- **One error model, three transports** — a single mapping table drives gRPC
  status, HTTP status, and MCP error envelopes. Only invalid-argument and
  unauthenticated errors forward their message; everything else surfaces a
  registry-safe message, and internal causes never leak.
- **AI-skills layer** — `ai/v1` annotations → provider-neutral IR →
  MCP / OpenAI-strict / Gemini tool schemas (golden-locked). The MCP bridge
  dispatches `tools/call` back through the **full interceptor chain** via an
  in-process loopback, so AI calls inherit auth/validation.
- **Batteries** — config (layered, fail-loud, masked secrets), generic DI,
  3-state health → gRPC Health, slog + OTLP, a PgBouncer-safe pgx pool + sqlc,
  and pluggable cache (Redis) / MQ (NATS, Kafka) abstractions.

## Quickstart

Install the dev toolchain — a Homebrew-style one-liner that corrects the Go env and installs the pinned tools (buf, protoc plugins, sqlc, …):

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
```

Install the CLI:

```bash
go install github.com/yshengliao/gortexa/cmd/gortexa@latest
```

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

## Developer CLI

`gortexa` (in `cmd/gortexa`) makes the framework batteries-included:

| Command | What |
|---|---|
| `gortexa create <path>` | Scaffold a new project (clone the layout, rewrite the module path). |
| `gortexa gen <domain>/<v> [Entity]` | Generate a CRUD API end-to-end: proto + logic stub + server wiring, then regenerate. |
| `gortexa regen` | Regenerate from proto (buf lint → breaking → generate). |
| `gortexa run` | Build and run the dev server. |
| `gortexa export --format=mcp\|openai\|gemini` | Export the project's `ai/v1` tool schemas as provider-ready JSON. |
| `gortexa tools install` / `sync` | Install / re-pin the dev toolchain (`tools/go.mod` directives). |
| `gortexa skills install` / `list` | Wire the AI-assist skills into Claude/Codex/Copilot/Antigravity. |
| `gortexa doctor` | Check the Go toolchain and proto tools. |

## Layout

| Path | What |
|---|---|
| `proto/` | Proto SSOT (`resource/v1`, `ai/v1`). Edit here, then `make gen`. |
| `gen/` | Generated code — **never hand-edit**, gitignored, produced by `make gen`. |
| `internal/` | Framework packages (kernel, interceptor, errors, httpcompat, mcp, …). |
| `cmd/server/` | Sample server wiring everything onto one port. |
| `cmd/gortexa/` | The `gortexa` developer CLI (create / gen / regen / run / tools / skills / doctor). |
| `tools/` | Pinned dev toolchain as go.mod `tool` directives (buf, protoc plugins, sqlc, …). |
| `.skills/` | Cross-tool AI skills (proto-regen, generating-apis, scaffolding-projects, …) for Claude/Codex/Copilot/Antigravity. |

## Notes

- Requires Go 1.26 (auto-downloaded via `GOTOOLCHAIN`). `make` exports the
  corrected module proxy env; run `install.sh` once if building outside `make`.
- Integration tests needing real PgBouncer/Kafka are behind the `integration`
  build tag (`make test-integration`); the default suite needs no services.

## Performance

Measured on **Go 1.26** with `go test -benchmem -count=8` (summarized with
`benchstat`) on a shared Intel Xeon @ 2.1 GHz. `allocs/op` and `B/op` are the machine-independent signals;
`ns/op` is indicative (shared CI CPU). Reproduce with
`go test -run='^$' -bench=. -benchmem -count=8 ./internal/...`.

**The Go 1.26 win — `errors.AsType` on the error hot path.** The three-transport
error resolver swapped the reflection-based `errors.As` for the new generic
`errors.AsType[*Error]`, removing one allocation per resolve. Same-toolchain A/B
(`go1.26.4`, `BenchmarkErrorResolve`, n=8, on the earlier 2.8 GHz host — the
durable result is the **allocation** reduction, which the current run above still
confirms at 104 B / 2 allocs):

| `resolve` via | ns/op | B/op | allocs/op |
|---|--:|--:|--:|
| `errors.As` (before) | 242.3 | 112 | 3 |
| `errors.AsType` (after) | 144.8 | 104 | **2** |
| **Δ** | **−40%** | **−7%** | **−33%** (p=0.000) |

The toolchain bump itself (Green Tea GC, faster `io.ReadAll`) is
allocation-neutral on the other hot paths — no regressions, with
the measurable framework win coming from the `errors.AsType` adoption above.

**Framework hot paths** (`go1.26.4`, `-benchmem -count=8`):

| Hot path | ns/op | B/op | allocs/op |
|---|--:|--:|--:|
| Error resolve → gRPC status (3-transport map) | ~105 | 104 | 2 |
| Rate-limiter `Allow` (sharded, serial) | ~197 | 0 | 0 |
| Rate-limiter `Allow` (parallel) | ~50 | 0 | 0 |
| MCP tool downgrade (per tool) | ~14 | 2 | 1 |
| MCP `tools/list` (memoized) | ~285 | 360 | 3 |
| Resource clone (proto deep-copy) | ~195 | 176 | 2 |
| Resource get (in-memory store) | ~214 | 176 | 2 |
| Full interceptor chain (8 stages, unary) | ~2,383 | 1,968 | 27 |

The paths that must never allocate (rate-limiter `Allow`) hold at **0 allocs/op**.
The fourth-round hardening is allocation-neutral on these paths (benchstat
before/after: rate-limiter `Allow` stays 0 allocs/op, the full interceptor chain
holds at 27 allocs/op — the added transport-boundary error normalization only
runs on the error path, so the success path is unchanged).
`BenchmarkBridgeHandlePost` separately drives a **512 KB** MCP request through the
full HTTP → JSON-RPC → dispatch path to exercise Go 1.26's faster `io.ReadAll`; at
that body size the read/parse allocation (~1.6 MB) dominates, so it measures
end-to-end large-body handling rather than `io.ReadAll` in isolation.

## Provenance

Gortexa was built with AI-assisted development and hardened through **four
independent model review rounds** — correctness, concurrency, security, and
protocol conformance — with every actionable finding fixed and verified
(`make build / vet / test -race / lint`):

| Model | Role |
|---|---|
| **Claude Fable 5** | Fourth full-codebase review, hardening fixes, and comprehensive test coverage |
| **Claude Opus 4.8** | Design, implementation, and consolidation |
| **Gemini 3.1 Pro** (Jules) | Second independent review |
| **Codex 5.5** | Third independent review |

## License

Released under the [MIT License](LICENSE).

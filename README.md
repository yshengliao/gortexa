# Gortexa

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Status](https://img.shields.io/badge/status-active-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![AI generated](https://img.shields.io/badge/AI%20generated-Opus%204.8%20%7C%20Gemini%203.1%20Pro%20%7C%20Codex%205.5-8A2BE2)](#provenance)

A contract-first, batteries-included **gRPC framework** for Go 1.26 — the gRPC
analogue of the `gortex` HTTP framework. Protobuf is the single source of truth;
**one h2c port** multiplexes three protocols:

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
  status, HTTP status, and MCP error envelopes. Internal errors never leak their
  cause.
- **AI-skills layer** — `ai/v1` annotations → provider-neutral IR →
  MCP / OpenAI-strict / Gemini tool schemas (golden-locked). The MCP bridge
  dispatches `tools/call` back through the **full interceptor chain** via an
  in-process loopback, so AI calls inherit auth/validation.
- **Batteries** — config (layered, fail-loud, masked secrets), generic DI,
  3-state health → gRPC Health, slog + OTLP, a PgBouncer-safe pgx pool + sqlc,
  and pluggable cache (Redis) / MQ (NATS, Kafka) abstractions.

## Quickstart

Install the dev toolchain with the one-line installer (Homebrew-style — corrects
the Go env, installs the pinned tools), then scaffold and run with the CLI:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/yshengliao/gortexa/main/install.sh)"
go install github.com/yshengliao/gortexa/cmd/gortexa@latest    # the gortexa CLI

gortexa create myapp --module github.com/me/myapp   # scaffold a batteries-included project
cd myapp
gortexa gen billing/v1 Invoice                      # proto + logic stub + wiring + codegen
gortexa run                                         # one h2c port on :8080
```

Or work in this repo directly with `make`:

```bash
make bootstrap   # install the pinned toolchain (tools/go.mod) + fix the Go env
make gen         # generate gRPC/gateway/OpenAPI from proto (buf lint → breaking → generate)
make test        # race tests with in-process fakes (bufconn/httptest/miniredis/embedded-nats)
make run         # start the sample server on :8080
```

Then, against the running server:

```bash
curl localhost:8080/healthz                                   # {"status":"ok"}
curl -XPOST localhost:8080/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
curl localhost:8080/v1/resources/x                            # 401 (auth shared with gRPC)
```

## Developer CLI

`gortexa` (in `cmd/gortexa`) makes the framework batteries-included:

| Command | What |
|---|---|
| `gortexa create <path>` | Scaffold a new project (clone the layout, rewrite the module path). |
| `gortexa gen <domain>/<v> [Entity]` | Generate a CRUD API end-to-end: proto + logic stub + server wiring, then regenerate. |
| `gortexa regen` | Regenerate from proto (buf lint → breaking → generate). |
| `gortexa run` | Build and run the dev server. |
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
| `cmd/gortexa/` | The `gortexa` developer CLI (create / gen / regen / run / tools / skills). |
| `tools/` | Pinned dev toolchain as go.mod `tool` directives (buf, protoc plugins, sqlc, …). |
| `.skills/` | Cross-tool AI skills (proto-regen, generating-apis, scaffolding-projects, …) for Claude/Codex/Copilot/Antigravity. |

## Notes

- Requires Go 1.26 (auto-downloaded via `GOTOOLCHAIN`). `make` exports the
  corrected module proxy env; run `install.sh` once if building outside `make`.
- Built and measured on **Go 1.26** (Green Tea GC, `errors.AsType`, `b.Loop`
  benchmarks): adopting `errors.AsType` drops an allocation on the error hot path
  (3→2 allocs, ~−33% time); the toolchain bump is otherwise allocation-neutral on
  the framework's measured hot paths.
- Integration tests needing real PgBouncer/Kafka are behind the `integration`
  build tag (`make test-integration`); the default suite needs no services.

## Provenance

Gortexa was built with AI-assisted development and hardened through **three
independent model review rounds** — correctness, concurrency, security, and
protocol conformance — with every actionable finding fixed and verified
(`make build / vet / test -race / lint`):

| Model | Role |
|---|---|
| **Claude Opus 4.8** | Design, implementation, and consolidation |
| **Gemini 3.1 Pro** (Jules) | Second independent review |
| **Codex 5.5** | Third independent review |

## License

Released under the [MIT License](LICENSE).

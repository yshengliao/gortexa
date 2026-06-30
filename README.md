# Gortexa

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Status](https://img.shields.io/badge/status-active-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![AI generated](https://img.shields.io/badge/AI%20generated-Opus%204.8%20%7C%20Gemini%203.1%20Pro%20%7C%20Codex%205.5-8A2BE2)](#provenance)

A contract-first, batteries-included **gRPC framework** for Go 1.25 — the gRPC
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

```bash
make bootstrap   # install buf + protoc plugins + sqlc, fix the Go env, pull go1.25
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

## Layout

| Path | What |
|---|---|
| `proto/` | Proto SSOT (`resource/v1`, `ai/v1`). Edit here, then `make gen`. |
| `gen/` | Generated code — **never hand-edit**, gitignored, produced by `make gen`. |
| `internal/` | Framework packages (kernel, interceptor, errors, httpcompat, mcp, …). |
| `cmd/server/` | Sample server wiring everything onto one port. |
| `.skills/proto-regen/` | Cross-tool AI skill for regenerating proto (Claude/Codex/Copilot/Antigravity). |

## Notes

- Requires Go 1.25 (auto-downloaded via `GOTOOLCHAIN`). `make` exports the
  corrected module proxy env; run `install.sh` once if building outside `make`.
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

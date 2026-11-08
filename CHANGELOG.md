# Changelog

All notable changes to this project are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(pre-1.0: a minor bump may break the API; every break is listed under
**Breaking**).

The `api/` module is versioned separately (`api/vX.Y.Z` tags); an entry that
touches it names the api version it needs.

## [Unreleased]

## [0.28.5] - 2026-11-08

Security patch release from the fifth independent review round: a
dependency audit plus a code review of every package, with 6 P1 (Medium) and
8 P2 (Low) findings fixed across #128–#138. Nothing in `api/` changed, so the
api module stays at `api/v0.28.0`. Companion documentation shipped in
gortexa-docs #27 (new limits and v0.28.5 pins), #28 (shutdown delay, readyz,
mq topics, AckWait), #29 (build fix) and #30 (Deployment → Security notes for
the informational findings). Follow-ups tracked as #140 (TLS for the redis
cache driver) and #141 (optional `iat` validation).

### Breaking

- kernel: the public port's `/readyz` body is now `{"status": ...}` only. The
  per-check inventory (`checks`, one entry per registered dependency with its
  live state) is served solely by the admin listener's `/readyz`
  (`kernel.WithAdminListener`); on the public port it listed every dependency
  by name and live state to unauthenticated callers. The `readiness not
  serving` log line still carries `checks`. (#138)

### Security

- cache: the in-memory backend is bounded by `cache.max_entries` (default
  100000; negative removes the bound). The default driver was an unbounded
  map, so a handler caching per-request-derived keys let a client grow the
  heap until OOM. A full cache evicts an existing entry (an expired one when
  found among the first few visited, otherwise an arbitrary one) to admit a
  new key; updating a live key never evicts. A negative TTL is refused as
  `InvalidArgument` instead of storing the entry forever. (#134)
- health: `Health.Watch` streams are also capped per client address (64;
  IPv6 keyed by its /64), under the existing global cap of 1024. Watch is
  unauthenticated and exempt from load shedding, so one source could fill
  every slot and sit on it, after which every other client's Watch failed
  with `ResourceExhausted` — which a gRPC client using client-side health
  checking reads as the backend being down. A stream with no network peer
  (an in-process caller) is subject to the global cap only. (#133)
- MCP: a JSON-RPC batch serves `tools/list` and `initialize` at most once
  each (further copies get a `-32600` element). Both are answered without
  Invoke, so the interceptor chain never governed them, and `tools/list`
  returns the whole catalogue: a batch of 100 was a ~45x unauthenticated
  response amplifier that re-marshalled every schema per element. The
  `tools/list` result is now serialized once. A string `id` longer than 256
  bytes is rejected like a numeric id longer than 32 characters, since the
  id is echoed and copied into a span attribute before the chain runs; the
  tracer provider also caps span attribute values at 4 KiB. (#132)
- interceptor: the validation cause the Logger records is bounded. The
  client-facing message was already capped at 10 violations / 512 bytes, but
  the full `*protovalidate.ValidationError` was still wrapped as the cause and
  rendered in the ERROR record one line per violation, so a request with a
  large repeated field produced a multi-megabyte log line per request. The
  cause is now the bounded rendering, and `error.cause` is truncated to 2 KiB
  for any wrapped error. (#131)
- auth: the JWKS refresh no longer runs under the key-set lock. A token
  naming an unknown kid triggers a refresh on the request path (once per 30s
  gap), and the HTTP fetch, bounded only by its 5s timeout, used to hold the
  mutex every other verification needs, so a slow issuer stalled all token
  checks, including those signed by known keys. The refresh gap is claimed
  under the lock, the fetch runs outside it, and concurrent callers are
  served from the current set meanwhile. (#130)
- kernel: external gRPC streams on the h2c port now have a first-message
  deadline (`server.grpc_first_message_timeout`, default 30s; negative
  disables). grpc-go's ServeHTTP mode reads the request message before the
  interceptor chain runs, so a client that sent HEADERS and nothing else held a
  handler goroutine that load shedding, rate limiting and auth never saw and
  that no server timeout ended (`read_timeout` is disabled by design,
  `idle_timeout` only fires on a stream-less connection). The deadline is
  cleared on the first body read, so slow bodies and long-lived streams are
  unaffected. The per-connection HTTP/2 stream budget is pinned to 250. (#129)
- Dependencies: `golang.org/x/net` v0.62.0 in the root module (GO-2026-6866,
  GO-2026-6860; `html` package, not reachable from gortexa). The `tools/`
  module moves to `golang.org/x/net` v0.62.0, `github.com/klauspost/compress`
  v1.20.3 and `golang.org/x/vuln` v1.9.0, closing 13 advisories that were
  reachable from the pinned dev tools (GO-2026-6617 HTTP/2 HPACK race,
  GO-2026-6865, and the Go 1.27.0/1.27.1 stdlib set). `tools/go.mod` now
  carries `toolchain go1.27.3` like the root module, so the dev tools build on
  the patched standard library (GO-2026-6809, GO-2026-6617). `api/go.mod` is
  left untouched: any change there is an api release, and the module has no
  reachable stdlib exposure. (#128)
- `make vuln` now also scans the `api/` module and the pinned tool binaries in
  `tools/` (`govulncheck tool`); previously only the root module was covered,
  which is why the tools advisories above went unnoticed by CI. (#128)
- mq: `Publish` on both drivers (and every JetStream topic) rejects a topic
  with a wildcard (`*`, `>`) or empty token as `InvalidArgument`; a topic
  built from request data could otherwise widen a subject. The core-NATS
  `Subscribe` deliberately still accepts wildcards. New
  `mq.ValidateLiteralTopic` exposes the check for request-derived topics. (#138)
- mq/jetstream: the consumer sets `AckWait` explicitly (30s) and the driver
  extends it (`InProgress`) every 15s while the handler runs, so a slow handler
  is not redelivered mid-flight and processed twice; the handler context is
  cancelled after 10 minutes so a hung handler cannot hold its ack-pending
  slot forever. (#138)
- kernel: see Breaking for the public `/readyz` body.
- config: a YAML integer that does not fit the target's sized integer field
  fails loading instead of being truncated (`db.max_conns: 4294967306` used
  to become 10 silently). `Secret` implements `encoding.TextMarshaler`, so
  yaml.v3, encoding/xml and encoding/csv emit the mask, not the raw value. (#137)
- MCP: `SetAllowedOrigins` swaps the browser-origin allowlist atomically; a
  reload after `Run` was a data race against every request and briefly
  rejected all browser origins. The default mask floor for
  `genai_capture_content` now covers the common spellings (access / refresh /
  id / api token, client secret, secret / private key, cookie, credentials,
  session id, password hash, x-api-key), and key folding also drops hyphens. (#137)
- cli: `gortexa create` no longer echoes `--repo` with embedded credentials
  to the console. (#137)
- apperr: the caller-provided message that `InvalidArgument` and
  `Unauthenticated` forward is capped at 4 KiB (rune-safe, marked). A handler
  embedding a request field in the message otherwise reflected an arbitrarily
  large value into the gRPC status trailer, the HTTP body and the MCP
  envelope. Applies to `*apperr.Error` and to a forwarded downstream status. (#136)
- storage: a typed-nil `*DBTracer` passed to `BuildPoolConfig`/`NewPool` is
  dropped instead of being wired into pgx, where it passed the interface
  nil-check and panicked on the first query. (#136)

### Added

- config: `server.shutdown_delay` (`ServerConfig.ShutdownDelay`, default `0s`)
  keeps the listener open, answering 503 / `NOT_SERVING`, between the
  readiness flip and the listener close, so load balancers observe the flip
  before connections are refused. It runs before the drain, so
  `shutdown_timeout` starts after it. (#138)
- mq: `ValidateLiteralTopic`. (#138)

### Changed

- interceptor: the circuit breaker treats gRPC codes it has no model for
  (`Aborted`, `ResourceExhausted`, `NotFound`, `Unknown`, `DataLoss`, …) as
  neutral instead of success, so they no longer reset the consecutive-failure
  counter and hide an outage. Failures remain `Unavailable`, `Internal` and
  `DeadlineExceeded`; a plain `error` still resolves to `Internal` and counts. (#136)

## [0.28.4] - 2026-11-04

### Breaking

- HTTP gateway: grpc-gateway v2.33.0 drops client-supplied `Grpc-Metadata-*`
  headers, including custom keys. Canonical `Authorization` and `X-Request-Id`
  headers continue to work. (#122)
- `apperr.Register`: a gRPC code may now be shared by several categories — the
  first registered owns it and the others travel across the loopback as an
  `ErrorInfo{Domain: "gortexa.apperr"}` status detail — instead of panicking on
  a duplicate code. Registering a category on `codes.OK` or `codes.Unknown` now
  panics. (#110)
- `(*apperr.Error).With` is copy-on-write: it returns a new `*Error` and leaves
  the receiver untouched, so code that called `err.With(k, v)` without using the
  result loses the field. (#110)
- MCP: `GET /mcp` answers `405 Method Not Allowed` (`Allow: POST`) instead of
  opening an idle SSE stream. (#110)
- MCP schema export: `OpenAISchema.Type` is now `any` (a string, or
  `["<type>","null"]` for nullable properties), `OpenAISchema.Enum` is `[]any`,
  and `GeminiSchema.AdditionalProperties` is removed (maps and Structs are
  described in prose for Gemini). (#110)
- `health.Registry`: new `Drain`, `Draining` and `CachedSnapshot`; `/readyz` and
  the gRPC health `Check`/`Watch` serve a shared snapshot re-evaluated at most
  once per watch interval, so a check's result is no longer fresh per probe.
  (#110)
- Rate limiter: IPv6 peers are keyed by their /64 (IPv4 and IPv4-mapped
  addresses as-is), and a full shard evicts a sampled least-recently-seen peer
  instead of rejecting the newcomer. (#110)
- sqlc `Querier` (`internal/storage/db`): `ListResources` takes a `page_token`
  (keyset pagination) and `UpdateResource` takes nullable arguments (a NULL
  leaves its column untouched), changing both parameter structs. (#110)
- `observability.SetupLogs` installs the logger it returns as the `slog`
  default. (#110)
- MCP: a request whose `MCP-Protocol-Version` header names an unsupported
  revision gets `400 Bad Request`, and JSON-RPC batches get `400` under
  2025-06-18 and later, which removed batching. Requests without the header are
  treated as 2025-03-26 and behave as before. (#112)

### Added

- `auth.NewJWKS`, `auth.NewStaticKeySet` and `auth.NewKeySetVerifier`: verify
  RS256 (RSA >= 2048) and ES256 (P-256) tokens by `kid` against a JWKS or a
  static key set. The JWKS fetch is https-only (http on loopback), capped and
  time-limited, refreshes on an unknown `kid` or after 15 minutes (at most once
  per 30s) and keeps the last good keys on failure. (#116)
- Config `auth.jwks_url` switches verification to the JWKS and makes
  `auth.jwt_secret` optional; `cmd/server` wires it. (#116)
- `scaffold` CI job: builds the CLI from the change, scaffolds a project from the
  checkout and runs its own gates, before and after `gortexa gen`. (#118)
- CI compatibility check: `gorelease` (pinned in `tools/go.mod`) compares the
  module against the latest release tag and fails a PR that breaks the API
  without a line under **Breaking** here. (#114)

### Changed

- MCP: supports revisions 2025-11-25 (now the default offered at
  `initialize`) and 2025-06-18 alongside 2025-03-26 and 2024-11-05, and
  validates the `MCP-Protocol-Version` header. (#112)
- Kernel shutdown drains readiness first (`/readyz` 503, gRPC health
  `NOT_SERVING`, `Watch` streams ended), concurrent `Shutdown` calls wait for
  the first, and each phase gets its own `ShutdownTimeout`. (#110)
- Health checks run concurrently under a per-check ceiling; a panicking check
  reports Unhealthy. (#110)
- OTel gRPC StatsHandler records unknown methods as `_OTHER` to bound metric
  cardinality; `log.level` also applies to the OTel log exporter. (#110)
- MCP tool schemas: properties not marked `ai_field.required` are nullable for
  OpenAI strict mode; tool names follow the MCP/OpenAI/Gemini intersection. (#110)
- JetStream bounds redelivery (max deliveries, growing back-off, InvalidArgument
  is terminal) and both NATS drivers log handler failures. (#110)
- `gortexa create` runs `go mod tidy` and `gofmt` on the scaffold and redacts
  credentials from the recorded source URL; `gortexa gen` rejects domain and
  entity names Go would treat specially. (#110, #118)
- The `gortexa:import` marker now ends the import block so inserted imports stay
  goimports-clean; scaffolds with the older layout keep working. (#118)

### Fixed

- `cmd/server`: exempt only the exact `grpc.health`/`grpc.reflection` services
  from auth, closing a bypass for user services in those namespaces. (#110)
- `apperr`: context cancellation and deadline errors map to Canceled and
  DeadlineExceeded instead of Internal; custom categories survive the loopback.
  (#110)
- HTTP gateway no longer forwards gRPC trailers, drops inbound
  `traceparent`/`tracestate`/`baggage` metadata, and CORS always sets
  `Vary: Origin` with an allowlist. (#110)
- Circuit breaker counts only calls admitted into the current closed episode.
  (#110)
- In-memory cache returns Unavailable after `Close`, matching Redis;
  `client.NewHTTPClient` defaults a negative timeout to 30s; the RESP client
  bounds bulk allocations. (#110)
- Config: comma lists are trimmed; a malformed dotenv file no longer echoes its
  contents in the error. (#110)
- `auth.Sign` rejects a non-positive TTL. (#110)
- CI catches an untidy `go.mod` and guards the api module steps; SLO alert and
  dev compose exposure fixed. (#110)

### Security

- Disable MPTCP on framework-created main and admin listeners, even when enabled
  by `GODEBUG`, to mitigate the demonstrated CVE-2026-98432 exploit. A patched
  Linux host kernel is still required; caller-provided listeners remain the
  caller's responsibility.
- Pin Go 1.27.3 and update `golang.org/x/net` to v0.60.0 to pick up the
  standard-library and HTTP/2 security fixes required by `govulncheck`. (#124)
- Closed the `grpc.*` namespace auth bypass, stopped leaking gRPC trailers and
  trusted trace baggage over HTTP, removed the unauthenticated MCP SSE stream,
  and kept dotenv secrets and URL credentials out of errors and manifests.
  (#110)

### Dependencies

- `google.golang.org/grpc` 1.83.1 → 1.83.2 in `tools/` (#117)
- `golang.org/x/time` 0.15.0 → 0.16.0 (#109)
- `buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go` →
  1.36.12-20260825204119-511051f7f437.2 (#108)
- `github.com/alicebob/miniredis/v2` 2.38.0 → 2.39.0 (#107)
- `github.com/knadh/koanf/parsers/dotenv` 1.1.1 → 1.1.2 (#106)
- `github.com/knadh/koanf/parsers/yaml` 1.1.0 → 1.1.1 (#105)
- Added `golang.org/x/exp/cmd/gorelease` to `tools/go.mod` (#114)

## [0.28.3]

Released 2026-09-06. Earlier history is in the
[GitHub releases](https://github.com/yshengliao/gortexa/releases) and the tag
compare links below.

[Unreleased]: https://github.com/yshengliao/gortexa/compare/v0.28.4...HEAD
[0.28.4]: https://github.com/yshengliao/gortexa/compare/v0.28.3...v0.28.4
[0.28.3]: https://github.com/yshengliao/gortexa/compare/v0.28.2...v0.28.3

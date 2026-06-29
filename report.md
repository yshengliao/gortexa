# Gortexa Independent Security & Concurrency Review

## Confirmed Bugs & Vulnerabilities

**Critical** | `internal/kernel/app.go:207` | **Data Race / Connection Leak in Shutdown vs Loopback**
- **Reasoning/Repro**: `App.Shutdown` reads `a.loopbackConn != nil` without locking `a.loopbackOnce`, while `App.Loopback()` writes to it concurrently via `Once.Do`. If `Shutdown` runs concurrently with `Loopback` initialization, it reads `nil`, skips closing the connection, and then `Loopback` finishes its assignment, permanently leaking the connection.
- **Suggested Fix**: Keep `loopbackConn` guarded by a mutex, or do the `Close()` inside a `loopbackOnce.Do` wrapper or by locking the same mutex used by `Once`. A simpler fix is to unconditionally track the connection if successfully created and lock around access.

**Critical** | `internal/kernel/app.go:214` | **Goroutine Leak on Bounded HTTP Server Shutdown**
- **Reasoning/Repro**: `App.Shutdown` uses `a.httpSrv.Shutdown(tctx)`. If `tctx` times out (e.g., due to long-running SSE GET requests in MCP `/mcp` which block on `<-r.Context().Done()`), `httpSrv.Shutdown` returns `context.DeadlineExceeded`, but it **does not** forcefully close the underlying connections. This leaks the connections and the goroutines serving them indefinitely.
- **Suggested Fix**: Fall back to `a.httpSrv.Close()` if `Shutdown` returns an error, just like the `grpcSrv` fallback.
```go
		if a.httpSrv != nil {
			if err := a.httpSrv.Shutdown(tctx); err != nil {
				a.httpSrv.Close() // Force-close remaining connections
				retErr = err
			}
		}
```

**High** | `internal/interceptor/ratelimit.go:58` | **Unbounded Map Growth & Algorithmic Complexity Attack**
- **Reasoning/Repro**: The per-IP rate limiter uses lazy sweep (`now.Sub(l.lastSweep) > l.ttl`). During a botnet/distributed surge, `l.entries` grows unbounded, risking OOM. Furthermore, after the surge ends and traffic stops for `ttl`, the *first* legitimate request to arrive will block on `l.mu.Lock()` and perform an `O(N)` loop iterating over millions of map entries to delete them, causing a massive latency spike for the entire server.
- **Suggested Fix**: Bounding the map size or switching to incremental eviction (e.g., deleting at most 1,000 stale entries per request) instead of scanning the entire map on one request. Alternatively, use a background goroutine for sweeping and limit the maximum number of entries to prevent OOM.

**High** | `internal/interceptor/circuitbreaker.go:73` | **Ignored `record()` resulting in lost failures**
- **Reasoning/Repro**: In `(b *breaker).record(success)`, the `switch b.state` is missing a `case cbOpen:` block. Although a trailing success might be safely ignored while open, if multiple concurrent requests are admitted during `cbHalfOpen`, the first failed probe transitions it back to `cbOpen`. Any subsequent probes that fail will hit `cbOpen` and just return without recording, which is technically safe, but if a trailing success arrives, it's also dropped and the breaker remains open when it might have otherwise helped evaluate the real health.
- **Suggested Fix**: Add `case cbOpen:` to handle late-arriving probe results, or explicitly acknowledge it as intentionally dropped.

**Medium** | `internal/mcp/downgrade.go:61` | **OpenAI Strict Schema Required Properties Bug**
- **Reasoning/Repro**: `toOpenAISchema` unconditionally sets `out.AdditionalProperties = &no`. However, if `s.Properties` is empty, it leaves `out.Required` empty. If OpenAI strictly expects `required: []` even for empty properties, omitting it could fail schema validation on the provider side.
- **Suggested Fix**: Explicitly set `out.Required = []string{}` if `len(s.Properties) == 0`.

## Verified Safe (Risky but Correct)
- **Error Model Leak**: Attempted to adversarially leak internal gRPC status causes in `Bridge.toolError()`. Verified that `reg.resolve` successfully intercepts pre-built internal gRPC statuses and defaults them to `SafeMessage`, preventing detail leakage.
- **HTTP/2 Cleartext Spoofing**: Analyzed `r.ProtoMajor == 2` coupled with `Content-Type: application/grpc`. Normal HTTP/1.1 clients spoofing the header are appropriately dropped to the Gateway (which then fails JSON processing) rather than breaking the native gRPC engine.
- **Circuit Breaker Concurrency**: The `cbHalfOpen` concurrent probe accounting correctly caps in-flight probes and gracefully handles concurrent failures and successes across the state machine.
- **PgBouncer Safety**: Confirmed that `pgx` is configured with `QueryExecModeExec` and zero-capacity caches. `sqlc` runs correctly under this transaction-mode configuration.
- **Interceptor Chain Order**: Verified that the fixed order and fail-loud panics on nil interceptors are strictly applied exactly as documented.
- **Auth Bypass via Loopback**: The `MCP` bridge forwards the authorization header and dials the internal loopback natively via `bufconn`. This correctly runs through the interceptor chain natively. Gateway headers are correctly bridged via `incomingHeaderMatcher`.
- **JWT `errors.Wrap` Leakage**: Inspected `Error.Error()` output format and its intersection with gRPC translation. It correctly scopes `e.cause` only for internal server logging, never returning it dynamically to clients.

// Package httplimits holds the request bounds and the metadata-safety predicate
// the framework's two inbound HTTP surfaces share.
//
// httpcompat (the grpc-gateway mux) and mcp (the JSON-RPC bridge) sit on the
// same h2c port, forward into the same loopback and face the same untrusted
// clients, so their caps have to agree. They used to be declared twice, with
// only a comment asserting the two were kept in sync and no test checking it —
// and the mcp copy was unexported, so httpcompat could not have referenced it
// even if someone tried.
package httplimits

import "time"

// MaxRequestBytes bounds an inbound request body on either HTTP surface.
const MaxRequestBytes = 1 << 20

// BodyReadTimeout bounds how long reading a size-capped body may take. The
// shared h2c server runs with ReadTimeout disabled so it cannot cut off
// long-lived gRPC or SSE streams, which leaves the short-bodied request paths
// without a body-read deadline; a slow-drip client could otherwise hold a
// connection open indefinitely. Applied around the body read only, then
// cleared, so a long streaming response is unaffected.
const BodyReadTimeout = 30 * time.Second

// PrintableASCII reports whether s is entirely 0x20-0x7E, the byte range gRPC
// permits in a metadata text value. Both surfaces filter with it before letting
// a client-supplied header reach outgoing metadata: an invalid byte there makes
// the loopback client fail the call with codes.Internal before the interceptor
// chain runs, so the caller gets an opaque 500 instead of the chain's verdict.
func PrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

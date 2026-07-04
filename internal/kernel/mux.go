package kernel

import (
	"net/http"
	"strings"
)

// newMux builds the single-port dispatch handler (without the h2c wrapper, so
// it is unit-testable). Decision order, cheapest checks first:
//  1. HTTP/2 + Content-Type application/grpc → native gRPC
//  2. /mcp                                   → MCP Streamable HTTP
//  3. /healthz, /readyz                      → health JSON
//  4. everything else                        → HTTP/JSON gateway
func newMux(grpcH, mcpH, gatewayH, healthz, readyz http.Handler) http.Handler {
	root := http.NewServeMux()
	if mcpH != nil {
		root.Handle("/mcp", mcpH)
	}
	root.Handle("/healthz", healthz)
	root.Handle("/readyz", readyz)
	if gatewayH != nil {
		root.Handle("/", gatewayH)
	} else {
		root.Handle("/", http.NotFoundHandler())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if grpcH != nil && r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			// grpcH is *grpc.Server, so this dispatches external gRPC through its
			// ServeHTTP handler mode — the price of multiplexing gRPC and HTTP on
			// one h2c port. grpc-go marks ServeHTTP Experimental: it rides Go's
			// net/http HTTP/2 stack, not grpc-go's own transport, so some
			// transport-level features (keepalive enforcement, some flow-control
			// and max-concurrent-stream behaviour) differ from a dedicated gRPC
			// port. The in-process loopback (gateway/MCP forwarding) uses grpc-go's
			// native transport instead, so those paths are unaffected. If a
			// deployment needs grpc-go's full transport semantics on the external
			// surface, split gRPC onto its own listener (e.g. via cmux) rather
			// than this shared port.
			grpcH.ServeHTTP(w, r)
			return
		}
		root.ServeHTTP(w, r)
	})
}

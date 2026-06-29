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
			grpcH.ServeHTTP(w, r)
			return
		}
		root.ServeHTTP(w, r)
	})
}

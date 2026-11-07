package kernel

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// firstMessageTimeout maps the config value to the deadline the gRPC branch
// arms: zero means the default, negative disables.
func firstMessageTimeout(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return defaultGRPCFirstMessageTimeout
	case d < 0:
		return 0
	default:
		return d
	}
}

// withFirstMessageDeadline arms a read deadline on an external gRPC stream
// that is cleared as soon as the first bytes of the request body arrive.
//
// grpc-go's ServeHTTP mode reads the request message before the interceptor
// chain runs, so a client that sends HEADERS and then nothing holds a handler
// goroutine (and an http2 stream) that load shedding, rate limiting and auth
// never see. The h2c server runs with ReadTimeout disabled (it would reset
// long-lived streams) and IdleTimeout only fires on a connection with no
// streams, so nothing else ends such a stream. When the deadline expires the
// http2 layer resets the stream; the body read fails and grpc-go tears the
// stream down. d <= 0 returns next unchanged.
func withFirstMessageDeadline(next http.Handler, d time.Duration) http.Handler {
	if d <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(d)); err != nil {
			// A writer without deadline support (a test recorder, a wrapper
			// that hides it): nothing to clear later either.
			next.ServeHTTP(w, r)
			return
		}
		body := &firstReadClears{ReadCloser: r.Body, clear: func() { _ = rc.SetReadDeadline(time.Time{}) }}
		r.Body = body
		// Clear on exit too: a handler that returns without reading (an
		// unknown method, a rejected content-type) must not leave the timer
		// armed against a stream the http2 layer is already closing.
		defer body.clearOnce()
		next.ServeHTTP(w, r)
	})
}

// firstReadClears is a request body that runs clear once, after the first
// Read that returns data.
type firstReadClears struct {
	io.ReadCloser
	clear   func()
	cleared atomic.Bool
}

func (b *firstReadClears) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.clearOnce()
	}
	return n, err
}

func (b *firstReadClears) clearOnce() {
	if b.cleared.CompareAndSwap(false, true) {
		b.clear()
	}
}

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

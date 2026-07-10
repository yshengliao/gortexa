// Package httpcompat builds the grpc-gateway HTTP/JSON layer: a ServeMux whose
// error handler funnels through internal/errors (so HTTP, gRPC and MCP all map
// errors identically and Internal never leaks), a protojson marshaler, and an
// incoming header matcher that forwards Authorization to the gRPC metadata key
// the auth interceptor reads — so HTTP and gRPC share one auth path.
package httpcompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

// MaxRequestBytes bounds a gateway JSON request body (1 MiB), matching the MCP
// bridge's own limit so the two HTTP surfaces cap request size consistently.
const MaxRequestBytes = 1 << 20

// bodyReadTimeout bounds how long reading a (size-capped) gateway body may take.
// The shared h2c server runs with ReadTimeout disabled so it can't cut off
// long-lived gRPC/SSE streams, which leaves the short-bodied gateway path
// without a body-read deadline — a slow-drip client could otherwise hold a
// connection open indefinitely. This is applied only around the body read and
// then cleared, so a long server-streaming gateway *response* is unaffected.
const bodyReadTimeout = 30 * time.Second

// MaxBodyBytes reads and caps the request body of the wrapped gateway handler.
// grpc-gateway decodes straight from r.Body with an unbounded json.Decoder, so
// without this a client could stream an arbitrarily large JSON body into memory
// (the MCP bridge already guards its own path). The body is read here — under a
// size cap and a read deadline — then replaced with an in-memory reader, so the
// deadline bounds only the read and never the handler's (possibly streaming)
// response. An over-declared Content-Length is rejected up front; an oversized
// body (declared or chunked) yields 413; a too-slow body yields 408.
func MaxBodyBytes(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxRequestBytes {
			writeStatusJSON(w, http.StatusRequestEntityTooLarge, "invalid_argument", "request body too large")
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) // best-effort; unsupported writers just skip it
		buf, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
		// Clear on every path — before the error response and before the handler
		// may stream — so a stale read deadline can't bleed onto a reused
		// keep-alive connection.
		_ = rc.SetReadDeadline(time.Time{})
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeStatusJSON(w, http.StatusRequestEntityTooLarge, "invalid_argument", "request body too large")
				return
			}
			// A read-deadline expiry (or client disconnect) mid-body: report a
			// timeout rather than letting it surface as a decode error.
			writeStatusJSON(w, http.StatusRequestTimeout, "deadline_exceeded", "request body read timed out")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(buf))
		r.ContentLength = int64(len(buf))
		h.ServeHTTP(w, r)
	})
}

func writeStatusJSON(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + message + `"}`))
}

// NewServeMux builds the gateway mux wired to the error registry.
func NewServeMux(reg *apperr.Registry) *runtime.ServeMux {
	return runtime.NewServeMux(
		runtime.WithErrorHandler(errorHandler(reg)),
		// Mid-stream errors go through the same registry as unary errors, so a
		// server-streaming handler can't serialize raw err.Error() text.
		runtime.WithStreamErrorHandler(func(_ context.Context, err error) *status.Status {
			return reg.ToGRPCStatus(err)
		}),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, jsonMarshaler()),
		runtime.WithIncomingHeaderMatcher(incomingHeaderMatcher),
		runtime.WithOutgoingHeaderMatcher(outgoingHeaderMatcher),
		runtime.WithRoutingErrorHandler(routingErrorHandler(reg)),
		runtime.WithMetadata(clientIPMetadata),
	)
}

// clientIPMetadata carries the gateway's observed client IP across the in-process
// loopback so the rate limiter (and audit logging) can key on the real peer
// instead of the synthetic "bufconn" address shared by all gateway traffic. It
// uses the gateway's own r.RemoteAddr — never an inbound X-Forwarded-For header,
// which an untrusted client could spoof (the header matcher also drops the
// dedicated key).
func clientIPMetadata(_ context.Context, r *http.Request) metadata.MD {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return nil
	}
	return metadata.Pairs(interceptor.PeerIPMetaKey, host)
}

func jsonMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: false},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// incomingHeaderMatcher forwards Authorization and X-Request-Id into gRPC
// metadata under the keys the interceptors expect; other headers use the
// gateway default — except anything that would land on the trusted peer-IP
// metadata key. The rate limiter trusts that key on the loopback (see
// interceptor.PeerIPMetaKey), and the gateway default forwards any
// "Grpc-Metadata-*" header verbatim, so without this guard an external client
// could spoof its rate-limit identity via "Grpc-Metadata-X-Gortexa-Peer-Ip".
func incomingHeaderMatcher(key string) (string, bool) {
	switch textproto.CanonicalMIMEHeaderKey(key) {
	case "Authorization":
		return auth.MetadataKey, true
	case "X-Request-Id":
		return interceptor.RequestIDMetadataKey, true
	default:
		k, ok := runtime.DefaultHeaderMatcher(key)
		if ok && strings.EqualFold(k, interceptor.PeerIPMetaKey) {
			return "", false
		}
		return k, ok
	}
}

// outgoingHeaderMatcher renders the request id the RequestID interceptor sets in
// gRPC metadata as a clean "X-Request-Id" response header instead of the gateway
// default "Grpc-Metadata-X-Request-Id" — the sole allowlisted key. Everything
// else is dropped: x-request-id is the only response metadata any interceptor
// or handler currently sets (interceptor/requestid.go is the sole producer),
// and the gateway default forwards all gRPC trailer/header metadata verbatim,
// which would silently expose internal interceptor/handler metadata as
// "Grpc-Metadata-*" response headers. Add future headers as explicit named
// cases here, not by widening the default.
func outgoingHeaderMatcher(key string) (string, bool) {
	if strings.EqualFold(key, interceptor.RequestIDMetadataKey) {
		return "X-Request-Id", true
	}
	return "", false
}

// routingErrorHandler preserves a 405 for a known path hit with the wrong method
// instead of the gateway default, which reports method-not-allowed as
// Unimplemented (HTTP 501).
func routingErrorHandler(reg *apperr.Registry) runtime.RoutingErrorHandlerFunc {
	return func(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, httpStatus int) {
		if httpStatus == http.StatusMethodNotAllowed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"code":"method_not_allowed","message":"method not allowed"}`))
			return
		}
		runtime.DefaultRoutingErrorHandler(ctx, mux, m, w, r, httpStatus)
	}
}

// errorHandler renders gateway errors via the shared 3-way mapping.
func errorHandler(reg *apperr.Registry) runtime.ErrorHandlerFunc {
	return func(ctx context.Context, _ *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		code, body := reg.ToHTTP(err)
		if rid := r.Header.Get("X-Request-Id"); interceptor.ValidRequestID(rid) {
			body.RequestID = rid
		}
		// The success path forwards the server-minted request id through
		// outgoingHeaderMatcher; this custom error handler bypasses that machinery,
		// so recover the id from the gRPC server metadata when the caller supplied
		// none. Without this, an error response to a caller that sent no inbound
		// X-Request-Id carries the id in neither the header nor the body, leaving the
		// client with nothing to correlate on. RequestID() sets the header metadata
		// before the handler runs, so it is present even on error/trailers-only.
		if body.RequestID == "" {
			if md, ok := runtime.ServerMetadataFromContext(ctx); ok {
				if vals := md.HeaderMD.Get(interceptor.RequestIDMetadataKey); len(vals) > 0 && interceptor.ValidRequestID(vals[0]) {
					body.RequestID = vals[0]
				}
			}
		}
		if body.RequestID != "" {
			w.Header().Set("X-Request-Id", body.RequestID)
		}
		w.Header().Set("Content-Type", m.ContentType(body))
		buf, mErr := m.Marshal(body)
		if mErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"internal","message":"internal error"}`))
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write(buf)
	}
}

// Package httpcompat builds the grpc-gateway HTTP/JSON layer: a ServeMux whose
// error handler funnels through internal/errors (so HTTP, gRPC and MCP all map
// errors identically and Internal never leaks), a protojson marshaler, and an
// incoming header matcher that forwards Authorization to the gRPC metadata key
// the auth interceptor reads — so HTTP and gRPC share one auth path.
package httpcompat

import (
	"context"
	"net"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

// NewServeMux builds the gateway mux wired to the error registry.
func NewServeMux(reg *apperr.Registry) *runtime.ServeMux {
	return runtime.NewServeMux(
		runtime.WithErrorHandler(errorHandler(reg)),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, jsonMarshaler()),
		runtime.WithIncomingHeaderMatcher(incomingHeaderMatcher),
		runtime.WithMetadata(clientIPMetadata),
	)
}

// clientIPMetadata carries the gateway's observed client IP across the in-process
// loopback so the rate limiter (and audit logging) can key on the real peer
// instead of the synthetic "bufconn" address shared by all gateway traffic. It
// uses the gateway's own r.RemoteAddr — never an inbound X-Forwarded-For header,
// which an untrusted client could spoof (the header matcher also drops it).
func clientIPMetadata(_ context.Context, r *http.Request) metadata.MD {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return nil
	}
	return metadata.Pairs(interceptor.ForwardedForMetaKey, host)
}

func jsonMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: false},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// incomingHeaderMatcher forwards Authorization and X-Request-Id into gRPC
// metadata under the keys the interceptors expect; other headers use the
// gateway default — except anything that would land on the trusted
// x-forwarded-for metadata key. The rate limiter trusts that key on the
// loopback (see interceptor.ForwardedForMetaKey), and the gateway default
// forwards any "Grpc-Metadata-*" header verbatim, so without this guard an
// external client could spoof its rate-limit identity (and flood the limiter
// with fabricated peers) via "Grpc-Metadata-X-Forwarded-For".
func incomingHeaderMatcher(key string) (string, bool) {
	switch textproto.CanonicalMIMEHeaderKey(key) {
	case "Authorization":
		return auth.MetadataKey, true
	case "X-Request-Id":
		return interceptor.RequestIDMetadataKey, true
	default:
		k, ok := runtime.DefaultHeaderMatcher(key)
		if ok && strings.EqualFold(k, interceptor.ForwardedForMetaKey) {
			return "", false
		}
		return k, ok
	}
}

// errorHandler renders gateway errors via the shared 3-way mapping.
func errorHandler(reg *apperr.Registry) runtime.ErrorHandlerFunc {
	return func(_ context.Context, _ *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		code, body := reg.ToHTTP(err)
		if rid := r.Header.Get("X-Request-Id"); interceptor.ValidRequestID(rid) {
			body.RequestID = rid
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

// Package httpcompat builds the grpc-gateway HTTP/JSON layer: a ServeMux whose
// error handler funnels through internal/errors (so HTTP, gRPC and MCP all map
// errors identically and Internal never leaks), a protojson marshaler, and an
// incoming header matcher that forwards Authorization to the gRPC metadata key
// the auth interceptor reads — so HTTP and gRPC share one auth path.
package httpcompat

import (
	"context"
	"net/http"
	"net/textproto"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
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
	)
}

func jsonMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: false},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// incomingHeaderMatcher forwards Authorization and X-Request-Id into gRPC
// metadata under the keys the interceptors expect; other headers use the
// gateway default.
func incomingHeaderMatcher(key string) (string, bool) {
	switch textproto.CanonicalMIMEHeaderKey(key) {
	case "Authorization":
		return auth.MetadataKey, true
	case "X-Request-Id":
		return interceptor.RequestIDMetadataKey, true
	default:
		return runtime.DefaultHeaderMatcher(key)
	}
}

// errorHandler renders gateway errors via the shared 3-way mapping.
func errorHandler(reg *apperr.Registry) runtime.ErrorHandlerFunc {
	return func(_ context.Context, _ *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		code, body := reg.ToHTTP(err)
		if rid := r.Header.Get("X-Request-Id"); rid != "" {
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

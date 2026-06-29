package interceptor

import (
	"buf.build/go/protovalidate"
	pvmw "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"google.golang.org/grpc"
)

// NewValidator builds a shared protovalidate validator (lazy rule compilation).
func NewValidator() (protovalidate.Validator, error) { return protovalidate.New() }

// Validation enforces buf.validate rules, returning InvalidArgument on failure.
// It is the innermost interceptor: only authenticated requests reach it.
func Validation(v protovalidate.Validator) grpc.UnaryServerInterceptor {
	return pvmw.UnaryServerInterceptor(v)
}

// ValidationStream is the streaming counterpart of Validation.
func ValidationStream(v protovalidate.Validator) grpc.StreamServerInterceptor {
	return pvmw.StreamServerInterceptor(v)
}

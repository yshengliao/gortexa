package interceptor

import (
	"context"
	"errors"
	"strings"

	"buf.build/go/protovalidate"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/observability"
)

// NewValidator builds a shared protovalidate validator (lazy rule compilation).
func NewValidator() (protovalidate.Validator, error) { return protovalidate.New() }

// validationDetails extracts the offending field name and a client-safe message
// from a protovalidate error. Violations describe the caller's own request (field
// names + rule messages), so they are safe to surface — they are not server
// internals.
func validationDetails(err error) (field, message string, ok bool) {
	verr, isVErr := errors.AsType[*protovalidate.ValidationError](err)
	if !isVErr || verr == nil {
		return "unknown", "validation failed", false
	}
	field, message = "unknown", strings.Join(strings.Fields(verr.Error()), " ")
	for _, v := range verr.Violations {
		if v.FieldDescriptor != nil {
			field = string(v.FieldDescriptor.Name())
			break
		}
	}
	return field, message, true
}

// failValidation maps a protovalidate error to the right category: a
// *ValidationError describes the caller's own request (safe to surface as
// InvalidArgument with the field-aware message and metric), while a
// CompilationError / RuntimeError is a server-side rule fault that maps to
// Internal with the cause unserialized — not blamed on the client.
func failValidation(ctx context.Context, gm *observability.GovernanceMetrics, method string, err error) error {
	field, message, ok := validationDetails(err)
	if !ok {
		return apperr.Wrap(apperr.CatInternal, "validation rule error", err)
	}
	if gm != nil {
		gm.ValidationFails.Add(ctx, 1, metric.WithAttributes(attribute.String("method", method), attribute.String("field", field)))
	}
	return apperr.Wrap(apperr.CatInvalidArgument, message, err)
}

// Validation enforces buf.validate rules, returning InvalidArgument on failure.
// It is the innermost interceptor: only authenticated requests reach it.
func Validation(v protovalidate.Validator, metrics ...*observability.GovernanceMetrics) grpc.UnaryServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			if err := v.Validate(msg); err != nil {
				return nil, failValidation(ctx, gm, info.FullMethod, err)
			}
		}
		return handler(ctx, req)
	}
}

// ValidationStream is the streaming counterpart of Validation.
func ValidationStream(v protovalidate.Validator, metrics ...*observability.GovernanceMetrics) grpc.StreamServerInterceptor {
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		wrapped := &validatingStream{ServerStream: ss, validator: v, metrics: gm, method: info.FullMethod}
		return handler(srv, wrapped)
	}
}

type validatingStream struct {
	grpc.ServerStream
	validator protovalidate.Validator
	metrics   *observability.GovernanceMetrics
	method    string
}

func (s *validatingStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	if msg, ok := m.(proto.Message); ok {
		if err := s.validator.Validate(msg); err != nil {
			return failValidation(s.Context(), s.metrics, s.method, err)
		}
	}
	return nil
}

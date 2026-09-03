package interceptor

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"buf.build/go/protovalidate"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/observability"
)

// NewValidator builds a shared protovalidate validator (lazy rule compilation).
func NewValidator() (protovalidate.Validator, error) { return protovalidate.New() }

// Bounds on the client-facing validation message. protovalidate reports one
// violation per offending element, so the violation count is controlled by the
// caller: an unbounded join turns a legal request with a large repeated field
// into a multi-megabyte message that InvalidArgument (one of the two categories
// apperr forwards verbatim) echoes on all three transports and that Logger
// copies into the ERROR record. The first few violations tell the caller what to
// fix; the ValidationFails metric carries the offending field for the rest.
const (
	maxViolationsInMessage = 10
	maxViolationBytes      = 512
)

// validationDetails extracts the offending field name and a client-safe message
// from a protovalidate error. Violations describe the caller's own request (field
// names + rule messages), so they are safe to surface — they are not server
// internals.
func validationDetails(err error) (field, message string, ok bool) {
	verr, isVErr := errors.AsType[*protovalidate.ValidationError](err)
	if !isVErr || verr == nil {
		return "unknown", "validation failed", false
	}
	field = "unknown"
	for _, v := range verr.Violations {
		if v.FieldDescriptor != nil {
			field = string(v.FieldDescriptor.Name())
			break
		}
	}
	return field, boundedViolations(verr.Violations), true
}

// boundedViolations renders the violations the way ValidationError.Error() does,
// but stops after maxViolationsInMessage / maxViolationBytes and reports the
// remainder as a count, so neither the message nor the transient allocation
// scales with the caller's element count.
func boundedViolations(violations []*protovalidate.Violation) string {
	if len(violations) == 0 {
		return "validation failed"
	}
	var b strings.Builder
	if len(violations) == 1 {
		b.WriteString("validation error: ")
	} else {
		b.WriteString("validation errors:")
	}
	shown := 0
	for _, v := range violations {
		if shown == maxViolationsInMessage || b.Len() >= maxViolationBytes {
			break
		}
		if len(violations) > 1 {
			b.WriteString(" - ")
		}
		b.WriteString(strings.Join(strings.Fields(v.String()), " "))
		shown++
	}
	msg := b.String()
	if len(msg) > maxViolationBytes {
		// Cut on a rune boundary: a truncated multi-byte rule message must not
		// put invalid UTF-8 on the wire (gRPC status strings must be valid UTF-8).
		msg = strings.ToValidUTF8(msg[:maxViolationBytes], "")
	}
	if rest := len(violations) - shown; rest > 0 {
		msg += " (+" + strconv.Itoa(rest) + " more)"
	}
	return msg
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

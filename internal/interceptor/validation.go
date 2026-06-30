package interceptor

import (
	"context"

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

func recordValidation(ctx context.Context, gm *observability.GovernanceMetrics, method string, err error) {
	if gm != nil && err != nil {
		gm.ValidationFails.Add(ctx, 1, metric.WithAttributes(attribute.String("method", method), attribute.String("field", "unknown")))
	}
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
				recordValidation(ctx, gm, info.FullMethod, err)
				return nil, apperr.Wrap(apperr.CatInvalidArgument, "validation failed", err)
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
			recordValidation(s.Context(), s.metrics, s.method, err)
			return apperr.Wrap(apperr.CatInvalidArgument, "validation failed", err)
		}
	}
	return nil
}

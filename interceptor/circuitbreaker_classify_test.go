package interceptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// TestBreakerUnclassifiedCodesAreNeutral pins classify's default branch. Codes
// the breaker has no opinion about (Aborted, ResourceExhausted, Unknown,
// NotFound, Unimplemented, DataLoss) used to count as success and reset the
// consecutive-failure counter, so a dependency outage interleaved with, say,
// NotFound lookups never opened the breaker. They say nothing about the
// downstream either way, so they must be neutral: not a failure, not a reset.
func TestBreakerUnclassifiedCodesAreNeutral(t *testing.T) {
	cb := NewCircuitBreaker(CBConfig{MaxFailures: 3, OpenInterval: time.Hour, HalfOpenMax: 1})
	const method = "/svc/Method"
	info := &grpc.UnaryServerInfo{FullMethod: method}
	call := func(err error) {
		_, _ = cb.Unary()(context.Background(), nil, info, func(context.Context, any) (any, error) { return nil, err })
	}
	down := apperr.New(apperr.CatUnavailable, "downstream dead")
	for _, neutral := range []error{
		status.Error(codes.Aborted, "retry"),
		status.Error(codes.ResourceExhausted, "quota"),
		status.Error(codes.NotFound, "missing"),
		status.Error(codes.DataLoss, "corrupt"),
	} {
		call(down)
		call(neutral)
	}
	// Four real failures with neutral calls between them: the counter must
	// have kept climbing and the breaker opened at MaxFailures.
	if b := cb.get(method); b.state != cbOpen {
		t.Fatalf("state = %v (failures=%d), want open: unclassified codes reset the failure counter", b.state, b.failures)
	}
}

// TestBreakerPlainErrorCountsAsFailure pins the documented consequence of the
// error model: an error that is not an *apperr.Error and carries no status
// resolves to Internal, which is a server fault, so it trips the breaker like
// any other Internal. Handlers that return client faults must wrap them in
// apperr so the breaker (and the client) see the right category.
func TestBreakerPlainErrorCountsAsFailure(t *testing.T) {
	cb := NewCircuitBreaker(CBConfig{MaxFailures: 2, OpenInterval: time.Hour, HalfOpenMax: 1})
	const method = "/svc/Method"
	info := &grpc.UnaryServerInfo{FullMethod: method}
	for range 2 {
		_, _ = cb.Unary()(context.Background(), nil, info, func(context.Context, any) (any, error) { return nil, errors.New("sql: no rows") })
	}
	if b := cb.get(method); b.state != cbOpen {
		t.Fatalf("state = %v, want open after %d plain errors", b.state, 2)
	}
}

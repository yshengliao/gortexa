package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
)

// TestCircuitBreaker_ClientCanceledDoesNotTripBreaker verifies that a caller
// cancelling its own request (ctx.Err() == context.Canceled surfacing from the
// handler) is a client-caused outcome and must NOT count toward tripping the
// per-method circuit breaker. If classify treated a laundered context.Canceled
// as a server failure (apperr.ToGRPCStatus launders it to Internal, a tripping
// category), five cancelled calls from one caller would open the breaker and an
// unrelated, healthy sixth caller would be denied with "circuit open".
func TestCircuitBreaker_ClientCanceledDoesNotTripBreaker(t *testing.T) {
	cb := NewCircuitBreaker(CBConfig{MaxFailures: 5, OpenInterval: 10_000_000_000, HalfOpenMax: 1})
	unary := cb.Unary()

	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Method/Do"}

	// Simulate a handler that forwards ctx.Err() once the caller cancels —
	// a very common shape (e.g. a pgx/HTTP call surfacing context.Canceled).
	cancelingHandler := func(ctx context.Context, req any) (any, error) {
		return nil, context.Canceled
	}

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := unary(ctx, nil, info, cancelingHandler)
		if err != context.Canceled {
			t.Fatalf("call %d: expected context.Canceled to pass through, got %v", i, err)
		}
	}

	// A brand-new, healthy caller now issues a normal request.
	healthyHandler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	resp, err := unary(context.Background(), nil, info, healthyHandler)
	if err != nil {
		t.Fatalf("unrelated healthy caller was denied: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response: %v", resp)
	}
}

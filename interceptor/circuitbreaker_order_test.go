package interceptor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// fakeAuthenticator rejects with Unauthenticated whenever ctx carries the
// authFailKey sentinel, and otherwise admits the call unchanged — enough to
// simulate a client sending no/expired credentials without pulling in the JWT
// machinery.
type fakeAuthenticator struct{}

type authFailKeyType struct{}

var authFailKey authFailKeyType

func (fakeAuthenticator) Authenticate(ctx context.Context) (context.Context, error) {
	if ctx.Value(authFailKey) != nil {
		return ctx, apperr.New(apperr.CatUnauthenticated, "missing authorization")
	}
	return ctx, nil
}

// TestCircuitBreakerRunsBeforeAuth_ClientErrorsCountAsSuccess locks in that a
// rejection produced by a stage *inside* the breaker (auth here, validation
// likewise) is neutral, not a successful probe. The chain order is fixed —
// circuitbreaker is outer, auth is inner — so an unauthenticated caller reaches
// b.allow() and can be admitted as the half-open probe. Scoring its
// Unauthenticated rejection as success would close the breaker over a real,
// ongoing downstream outage, letting an unauthenticated attacker pin every
// method's breaker closed.
func TestCircuitBreakerRunsBeforeAuth_ClientErrorsCountAsSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cb := NewCircuitBreaker(CBConfig{MaxFailures: 1, OpenInterval: time.Second, HalfOpenMax: 1})
		authIC := AuthWith(fakeAuthenticator{}, nil)

		const method = "/svc/Method"
		info := &grpc.UnaryServerInfo{FullMethod: method}

		// The chain order under test: circuitbreaker wraps auth wraps handler —
		// exactly chain.go's {"circuitbreaker", ...}, {"auth", ...} ordering.
		call := func(ctx context.Context, handler grpc.UnaryHandler) (any, error) {
			return cb.Unary()(ctx, nil, info, func(ctx2 context.Context, req2 any) (any, error) {
				return authIC(ctx2, req2, info, handler)
			})
		}

		// 1) A real downstream failure trips the breaker (MaxFailures=1).
		failingHandler := func(ctx context.Context, req any) (any, error) {
			return nil, apperr.New(apperr.CatInternal, "downstream dead")
		}
		if _, err := call(context.Background(), failingHandler); err == nil {
			t.Fatal("expected the downstream failure to propagate")
		}
		b := cb.get(method)
		if b.state != cbOpen {
			t.Fatalf("state after 1 real failure = %v, want open (MaxFailures=1)", b.state)
		}

		// 2) Let OpenInterval elapse so the breaker will admit a half-open probe.
		time.Sleep(2 * time.Second)

		// 3) An unauthenticated caller arrives. It is admitted as the probe (auth
		// runs inside the breaker) but must not resolve the episode. A handler that
		// fails the test proves the call never gets past auth.
		unauthHandler := func(ctx context.Context, req any) (any, error) {
			t.Fatal("handler reached: auth should have rejected the unauthenticated call")
			return nil, nil
		}
		authCtx := context.WithValue(context.Background(), authFailKey, true)
		_, err := call(authCtx, unauthHandler)
		if err == nil {
			t.Fatal("expected the unauthenticated call to be rejected")
		}
		if got := apperr.ToGRPCStatus(err); got.Code().String() != "Unauthenticated" {
			t.Fatalf("rejection code = %v, want Unauthenticated", got.Code())
		}

		// 4) The client-caused rejection is no evidence of downstream health, so the
		// breaker must still be shielding the outage.
		if b.state == cbClosed {
			t.Fatalf("circuit breaker closed by an unauthenticated rejection: state=%v, failures=%d "+
				"(a client-caused Unauthenticated error was scored as a successful probe and healed "+
				"the breaker over a real, ongoing downstream outage)",
				b.state, b.failures)
		}

		// 5) The probe slot is still released, so a genuine probe can follow and a
		// successful one closes the breaker as usual.
		if b.probes != 0 {
			t.Fatalf("neutral probe leaked its slot: probes=%d", b.probes)
		}
		if _, err := call(context.Background(), func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
			t.Fatalf("genuine probe was denied: %v", err)
		}
		if b.state != cbClosed {
			t.Fatalf("state = %v, want closed after a successful probe", b.state)
		}
	})
}

// TestCircuitBreakerClientErrorsDoNotResetFailureCounter locks in the closed-state
// half of the same invariant: client-caused rejections interleaved into a genuine
// outage must not keep clearing the consecutive-failure counter, or the breaker
// never opens.
func TestCircuitBreakerClientErrorsDoNotResetFailureCounter(t *testing.T) {
	cb := NewCircuitBreaker(CBConfig{MaxFailures: 5, OpenInterval: time.Hour, HalfOpenMax: 1})
	authIC := AuthWith(fakeAuthenticator{}, nil)

	const method = "/svc/Method"
	info := &grpc.UnaryServerInfo{FullMethod: method}
	call := func(ctx context.Context, handler grpc.UnaryHandler) (any, error) {
		return cb.Unary()(ctx, nil, info, func(ctx2 context.Context, req2 any) (any, error) {
			return authIC(ctx2, req2, info, handler)
		})
	}
	failingHandler := func(context.Context, any) (any, error) {
		return nil, apperr.New(apperr.CatInternal, "downstream dead")
	}
	unauthCtx := context.WithValue(context.Background(), authFailKey, true)

	for range 5 {
		if _, err := call(context.Background(), failingHandler); err == nil {
			t.Fatal("expected the downstream failure to propagate")
		}
		// One unauthenticated request between every real failure.
		if _, err := call(unauthCtx, failingHandler); err == nil {
			t.Fatal("expected the unauthenticated call to be rejected")
		}
	}

	b := cb.get(method)
	if b.state != cbOpen {
		t.Fatalf("state = %v (failures=%d), want open: unauthenticated traffic interleaved into a "+
			"real outage kept resetting the consecutive-failure counter", b.state, b.failures)
	}
}

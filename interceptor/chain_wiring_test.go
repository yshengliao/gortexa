package interceptor_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/interceptor"
)

// The rate-limit and circuit-breaker stages are covered in isolation elsewhere;
// these two tests pin the wiring NewSet does — that cfg.RateLimit/cfg.CircuitBreaker
// reach the constructors and that each stage's closure lands in its own Set field.
// Cross-wiring the two (same type, compiles clean) or dropping the config leaves
// non-nil closures, so UnaryChain's fail-loud nil check cannot catch it.

// TestNewSetWiresRateLimitStage drives the composed chain from one peer past its
// configured budget: the limiter runs outside auth, so unauthenticated calls
// still have to be refused with ResourceExhausted rather than Unauthenticated.
func TestNewSetWiresRateLimitStage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		set, err := interceptor.NewSet(interceptor.Config{
			Verifier:  auth.MustNewVerifier(jwtSecret, "gortexa"),
			RateLimit: interceptor.RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute},
		})
		if err != nil {
			t.Fatal(err)
		}
		chain := set.ChainUnary()
		ctx := peerCtx("9.9.9.9:1234")

		var limited int
		for range 20 {
			_, err := chain(ctx, nil, unaryInfo("/svc/M"), okHandler)
			if status.Code(err) == codes.ResourceExhausted {
				limited++
			}
		}
		if limited == 0 {
			t.Fatal("RPS=1/Burst=1: 20 calls from one peer produced no ResourceExhausted — rate-limit stage not wired into the chain")
		}
	})
}

// TestNewSetWiresCircuitBreakerStage pins the breaker to the composed chain: once
// the configured consecutive-failure budget is spent the handler must stop being
// reached, which distinguishes a live breaker from the handler's own Unavailable.
func TestNewSetWiresCircuitBreakerStage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := auth.MustNewVerifier(jwtSecret, "gortexa")
		set, err := interceptor.NewSet(interceptor.Config{
			Verifier:       v,
			CircuitBreaker: interceptor.CBConfig{MaxFailures: 2, OpenInterval: time.Minute, HalfOpenMax: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := v.Sign("u-1", nil, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		// The breaker sits outside auth, so the call must actually authenticate
		// for the handler's failure to be counted.
		ctx := metadata.NewIncomingContext(peerCtx("9.9.9.9:1234"), metadata.Pairs(auth.MetadataKey, "Bearer "+tok))

		var handlerRuns int
		down := func(context.Context, any) (any, error) {
			handlerRuns++
			return nil, apperr.New(apperr.CatUnavailable, "dependency down")
		}
		chain := set.ChainUnary()
		for range 5 {
			if _, err := chain(ctx, nil, unaryInfo("/svc/M"), down); status.Code(err) != codes.Unavailable {
				t.Fatalf("call err = %v, want Unavailable", err)
			}
		}
		if handlerRuns != 2 {
			t.Fatalf("handler ran %d times over 5 calls with MaxFailures=2, want 2 — circuit-breaker stage not wired into the chain", handlerRuns)
		}
	})
}

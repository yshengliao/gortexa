package interceptor_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
)

func TestValidRequestID(t *testing.T) {
	for _, ok := range []string{"abc-123", "a", strings.Repeat("a", 128), "req_1.2-3"} {
		if !interceptor.ValidRequestID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 129), "has space", "new\nline", "emoji🙂", "semi;colon"} {
		if interceptor.ValidRequestID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

// An invalid/oversized inbound X-Request-Id is dropped and a fresh id is minted,
// rather than propagated and reflected.
func TestRequestIDDropsInvalidInbound(t *testing.T) {
	ic := interceptor.RequestID()
	bad := strings.Repeat("x", 200)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(interceptor.RequestIDMetadataKey, bad))
	var seen string
	_, _ = ic(ctx, nil, unaryInfo("/svc/M"), func(c context.Context, _ any) (any, error) {
		seen, _ = interceptor.RequestIDFrom(c)
		return nil, nil
	})
	if seen == bad || seen == "" {
		t.Fatalf("invalid inbound id should be replaced by a fresh one, got %q", seen)
	}
}

var jwtSecret = []byte("0123456789abcdef0123456789abcdef")

func okHandler(_ context.Context, _ any) (any, error) { return "ok", nil }

func unaryInfo(method string) *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: method}
}

type fakeAddr string

func (f fakeAddr) Network() string { return "tcp" }
func (f fakeAddr) String() string  { return string(f) }

func peerCtx(addr string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: fakeAddr(addr)})
}

func TestRecoveryConvertsPanic(t *testing.T) {
	ic := interceptor.Recovery(nil)
	_, err := ic(context.Background(), nil, unaryInfo("/svc/M"), func(context.Context, any) (any, error) {
		panic("boom")
	})
	// Recovery normalizes to a transport status; a panic must map to Internal
	// and must not leak the panic value ("boom").
	st := apperr.ToGRPCStatus(err)
	if st.Code().String() != "Internal" {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
	if st.Message() != "internal error" {
		t.Fatalf("message = %q, want safe internal message", st.Message())
	}
}

// TestRecoveryNormalizesHandlerErrors verifies the transport-boundary mapping:
// a plain error and an fmt-wrapped *Error returned by a handler are reduced
// through the registry so the client never sees codes.Unknown or a leaked cause.
func TestRecoveryNormalizesHandlerErrors(t *testing.T) {
	ic := interceptor.Recovery(nil)

	// A plain error would otherwise reach the client as codes.Unknown + raw text.
	_, err := ic(context.Background(), nil, unaryInfo("/svc/M"), func(context.Context, any) (any, error) {
		return nil, stderrors.New("raw internal detail")
	})
	st := apperr.ToGRPCStatus(err)
	if st.Code().String() != "Internal" || st.Message() != "internal error" {
		t.Fatalf("plain error → %v %q, want Internal 'internal error'", st.Code(), st.Message())
	}

	// A fmt-wrapped *Error (not itself an *Error, so grpc-go wouldn't call
	// GRPCStatus) must still resolve to its category, not leak the wrapper text.
	wrapped := fmt.Errorf("context: %w", apperr.New(apperr.CatNotFound, "resource 42 missing"))
	_, err = ic(context.Background(), nil, unaryInfo("/svc/M"), func(context.Context, any) (any, error) {
		return nil, wrapped
	})
	if st := apperr.ToGRPCStatus(err); st.Code().String() != "NotFound" {
		t.Fatalf("wrapped *Error → %v, want NotFound", st.Code())
	}
}

func TestRequestIDMintsAndStores(t *testing.T) {
	ic := interceptor.RequestID()
	var seen string
	_, err := ic(context.Background(), nil, unaryInfo("/svc/M"), func(ctx context.Context, _ any) (any, error) {
		id, ok := interceptor.RequestIDFrom(ctx)
		if ok {
			seen = id
		}
		return nil, nil
	})
	if err != nil || seen == "" {
		t.Fatalf("request id not propagated: seen=%q err=%v", seen, err)
	}
}

func TestAuthInterceptor(t *testing.T) {
	v := auth.MustNewVerifier(jwtSecret, "gortexa")
	ic := interceptor.Auth(v, nil)
	info := unaryInfo("/svc/M")

	// missing metadata → Unauthenticated
	if _, err := ic(context.Background(), nil, info, okHandler); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("missing auth err = %v", err)
	}

	// valid token → handler runs, claims present
	tok, _ := v.Sign("u-1", []string{"admin"}, time.Hour)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(auth.MetadataKey, "Bearer "+tok))
	var gotSub string
	_, err := ic(ctx, nil, info, func(c context.Context, _ any) (any, error) {
		if cl, ok := auth.ClaimsFrom(c); ok {
			gotSub = cl.Subject
		}
		return "ok", nil
	})
	if err != nil || gotSub != "u-1" {
		t.Fatalf("valid auth: sub=%q err=%v", gotSub, err)
	}

	// skip func bypasses auth
	skip := interceptor.Auth(v, func(string) bool { return true })
	if _, err := skip(context.Background(), nil, info, okHandler); err != nil {
		t.Fatalf("skip should bypass: %v", err)
	}
}

func TestRateLimiterRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl := interceptor.NewRateLimiter(interceptor.RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})
		ic := rl.Unary()
		info := unaryInfo("/svc/M")
		ctx := peerCtx("1.2.3.4:1")

		if _, err := ic(ctx, nil, info, okHandler); err != nil {
			t.Fatalf("first call should pass: %v", err)
		}
		if _, err := ic(ctx, nil, info, okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
			t.Fatalf("second call err = %v, want ResourceExhausted", err)
		}
		time.Sleep(time.Second) // refill one token
		if _, err := ic(ctx, nil, info, okHandler); err != nil {
			t.Fatalf("after refill should pass: %v", err)
		}
	})
}

func TestRateLimiterKeysByIPNotPort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl := interceptor.NewRateLimiter(interceptor.RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})
		ic := rl.Unary()
		info := unaryInfo("/svc/M")
		// Same client IP, different ephemeral ports must share one bucket
		// (otherwise reconnecting would bypass the limit).
		if _, err := ic(peerCtx("5.5.5.5:1111"), nil, info, okHandler); err != nil {
			t.Fatalf("first call should pass: %v", err)
		}
		if _, err := ic(peerCtx("5.5.5.5:2222"), nil, info, okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
			t.Fatalf("second call from same IP/different port should be limited: %v", err)
		}
	})
}

func TestCircuitBreakerHalfOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cb := interceptor.NewCircuitBreaker(interceptor.CBConfig{MaxFailures: 2, OpenInterval: time.Second, HalfOpenMax: 1})
		ic := cb.Unary()
		info := unaryInfo("/svc/M")
		failHandler := func(context.Context, any) (any, error) {
			return nil, apperr.New(apperr.CatUnavailable, "down")
		}

		// two server-side failures trip the breaker
		_, _ = ic(context.Background(), nil, info, failHandler)
		_, _ = ic(context.Background(), nil, info, failHandler)

		// open: handler not called, fast-fail
		called := false
		probe := func(context.Context, any) (any, error) { called = true; return "ok", nil }
		_, err := ic(context.Background(), nil, info, probe)
		if !apperr.Is(err, apperr.CatUnavailable) || called {
			t.Fatalf("open state: err=%v called=%v", err, called)
		}

		// after the open interval, half-open admits one probe; success closes it
		time.Sleep(2 * time.Second)
		called = false
		if _, err := ic(context.Background(), nil, info, probe); err != nil || !called {
			t.Fatalf("half-open probe: err=%v called=%v", err, called)
		}
		// closed again
		if _, err := ic(context.Background(), nil, info, okHandler); err != nil {
			t.Fatalf("closed state err=%v", err)
		}
	})
}

// A panicking handler must still be recorded by the breaker: panics unwind past
// the breaker frame to Recovery, so without a deferred record they would never
// trip the breaker and a half-open probe would leak its slot (wedging the
// method open forever).
func TestCircuitBreakerRecordsPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cb := interceptor.NewCircuitBreaker(interceptor.CBConfig{MaxFailures: 2, OpenInterval: time.Second, HalfOpenMax: 1})
		ic := cb.Unary()
		info := unaryInfo("/svc/M")
		panicky := grpc.UnaryHandler(func(context.Context, any) (any, error) { panic("boom") })

		callRecover := func(h grpc.UnaryHandler) {
			defer func() { _ = recover() }() // the breaker re-panics for Recovery
			_, _ = ic(context.Background(), nil, info, h)
		}

		// Two panics trip the breaker (panic → Internal, a failing category).
		callRecover(panicky)
		callRecover(panicky)

		called := false
		probe := func(context.Context, any) (any, error) { called = true; return "ok", nil }
		if _, err := ic(context.Background(), nil, info, probe); !apperr.Is(err, apperr.CatUnavailable) || called {
			t.Fatalf("breaker should be open after panics: err=%v called=%v", err, called)
		}

		// A half-open probe that panics must release its slot so the breaker can
		// recover, rather than wedging the method permanently.
		time.Sleep(2 * time.Second)
		callRecover(panicky) // half-open probe panics
		time.Sleep(2 * time.Second)
		called = false
		if _, err := ic(context.Background(), nil, info, probe); err != nil || !called {
			t.Fatalf("breaker wedged after panicking probe: err=%v called=%v", err, called)
		}
	})
}

func TestLoadShedding(t *testing.T) {
	// concurrency signal
	block := make(chan struct{})
	s := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxInflight: 1})
	ic := s.Unary()
	info := unaryInfo("/svc/M")
	started := make(chan struct{})
	go func() {
		_, _ = ic(context.Background(), nil, info, func(context.Context, any) (any, error) {
			close(started)
			<-block
			return "ok", nil
		})
	}()
	<-started
	if _, err := ic(context.Background(), nil, info, okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("concurrency shed err = %v, want ResourceExhausted", err)
	}
	close(block)

	// cpu signal
	hot := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxCPU: 0.8, CPUSampler: func() float64 { return 0.95 }})
	if _, err := hot.Unary()(context.Background(), nil, info, okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("cpu shed err = %v, want ResourceExhausted", err)
	}
}

func TestValidationInterceptor(t *testing.T) {
	v, err := interceptor.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	ic := interceptor.Validation(v)
	info := unaryInfo("/resource.v1.ResourceService/GetResource")

	// empty id violates string.min_len = 1
	if _, err := ic(context.Background(), &resourcev1.GetResourceRequest{Id: ""}, info, okHandler); !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("invalid request should be rejected as InvalidArgument, got %v", err)
	}
	if _, err := ic(context.Background(), &resourcev1.GetResourceRequest{Id: "abc"}, info, okHandler); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

type orderKey struct{}

func TestChainUnaryOrder(t *testing.T) {
	record := func(tag string) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if p, ok := ctx.Value(orderKey{}).(*[]string); ok {
				*p = append(*p, tag)
			}
			return h(ctx, req)
		}
	}
	set := interceptor.Set{
		Recovery:       record("recovery"),
		RequestID:      record("requestid"),
		Logger:         record("logger"),
		LoadShedding:   record("loadshedding"),
		RateLimit:      record("ratelimit"),
		CircuitBreaker: record("circuitbreaker"),
		Auth:           record("auth"),
		Validation:     record("validation"),
	}
	var seq []string
	ctx := context.WithValue(context.Background(), orderKey{}, &seq)
	if _, err := set.ChainUnary()(ctx, nil, unaryInfo("/svc/M"), okHandler); err != nil {
		t.Fatal(err)
	}
	want := []string{"recovery", "requestid", "logger", "loadshedding", "ratelimit", "circuitbreaker", "auth", "validation"}
	if len(seq) != len(want) {
		t.Fatalf("order = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full %v)", i, seq[i], want[i], seq)
		}
	}
}

func TestUnaryChainFailLoud(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when a required interceptor is nil")
		}
	}()
	(interceptor.Set{}).UnaryChain()
}

func TestNewSetBuildsChains(t *testing.T) {
	set, err := interceptor.NewSet(interceptor.Config{
		Verifier: auth.MustNewVerifier(jwtSecret, "gortexa"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.UnaryChain()) != 8 || len(set.StreamChain()) != 8 {
		t.Fatalf("chains = %d/%d, want 8/8", len(set.UnaryChain()), len(set.StreamChain()))
	}
}

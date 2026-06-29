package interceptor_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

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
	if !apperr.Is(err, apperr.CatInternal) {
		t.Fatalf("err = %v, want Internal", err)
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
	v := auth.NewVerifier(jwtSecret, "gortexa")
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
	if _, err := ic(context.Background(), &resourcev1.GetResourceRequest{Id: ""}, info, okHandler); !apperr.Is(err, apperr.CatInvalidArgument) && err == nil {
		t.Fatalf("invalid request should be rejected, got %v", err)
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
		Verifier: auth.NewVerifier(jwtSecret, "gortexa"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.UnaryChain()) != 8 || len(set.StreamChain()) != 8 {
		t.Fatalf("chains = %d/%d, want 8/8", len(set.UnaryChain()), len(set.StreamChain()))
	}
}

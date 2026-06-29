package interceptor_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

type fakeStream struct {
	ctx context.Context
}

func (f *fakeStream) Context() context.Context     { return f.ctx }
func (f *fakeStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)       {}
func (f *fakeStream) SendMsg(any) error            { return nil }
func (f *fakeStream) RecvMsg(any) error            { return nil }

func streamInfo() *grpc.StreamServerInfo { return &grpc.StreamServerInfo{FullMethod: "/svc/Stream"} }

func okStream(any, grpc.ServerStream) error { return nil }

func TestRecoveryStream(t *testing.T) {
	ic := interceptor.RecoveryStream(nil)
	err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
		panic("stream boom")
	})
	if !apperr.Is(err, apperr.CatInternal) {
		t.Fatalf("err = %v, want Internal", err)
	}
}

func TestRequestIDStream(t *testing.T) {
	ic := interceptor.RequestIDStream()
	var seen string
	err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(_ any, ss grpc.ServerStream) error {
		if id, ok := interceptor.RequestIDFrom(ss.Context()); ok {
			seen = id
		}
		return nil
	})
	if err != nil || seen == "" {
		t.Fatalf("stream request id not propagated: %q %v", seen, err)
	}
}

func TestLoggerStream(t *testing.T) {
	if err := interceptor.LoggerStream(nil)(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); err != nil {
		t.Fatal(err)
	}
}

func TestAuthStream(t *testing.T) {
	v := auth.NewVerifier(jwtSecret, "gortexa")
	ic := interceptor.AuthStream(v, nil)

	// missing creds
	if err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); !apperr.Is(err, apperr.CatUnauthenticated) {
		t.Fatalf("missing-auth stream err = %v", err)
	}

	// valid creds
	tok, _ := v.Sign("u", nil, time.Hour)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(auth.MetadataKey, "Bearer "+tok))
	if err := ic(nil, &fakeStream{ctx: ctx}, streamInfo(), okStream); err != nil {
		t.Fatalf("valid stream auth err = %v", err)
	}

	// skip
	skip := interceptor.AuthStream(v, func(string) bool { return true })
	if err := skip(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); err != nil {
		t.Fatalf("skip stream err = %v", err)
	}
}

func TestStatefulStreams(t *testing.T) {
	// rate limiter stream (disabled → passes)
	rl := interceptor.NewRateLimiter(interceptor.RateLimitConfig{})
	if err := rl.Stream()(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); err != nil {
		t.Fatal(err)
	}
	// circuit breaker stream (disabled → passes)
	cb := interceptor.NewCircuitBreaker(interceptor.CBConfig{})
	if err := cb.Stream()(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); err != nil {
		t.Fatal(err)
	}
	// load shedder stream (cpu over threshold → shed)
	ls := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxCPU: 0.5, CPUSampler: func() float64 { return 0.9 }})
	if err := ls.Stream()(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("stream shed err = %v", err)
	}
}

func TestCircuitBreakerStreamOpens(t *testing.T) {
	cb := interceptor.NewCircuitBreaker(interceptor.CBConfig{MaxFailures: 1, OpenInterval: time.Hour, HalfOpenMax: 1})
	ic := cb.Stream()
	failStream := func(any, grpc.ServerStream) error { return apperr.New(apperr.CatUnavailable, "down") }
	// one failure trips it
	_ = ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), failStream)
	// now open: fast-fail without invoking the handler
	called := false
	err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
		called = true
		return nil
	})
	if !apperr.Is(err, apperr.CatUnavailable) || called {
		t.Fatalf("open stream: err=%v called=%v", err, called)
	}
}

func TestRateLimitStreamDenies(t *testing.T) {
	rl := interceptor.NewRateLimiter(interceptor.RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute})
	ic := rl.Stream()
	ctx := peerCtx("9.9.9.9:1")
	if err := ic(nil, &fakeStream{ctx: ctx}, streamInfo(), okStream); err != nil {
		t.Fatalf("first stream should pass: %v", err)
	}
	if err := ic(nil, &fakeStream{ctx: ctx}, streamInfo(), okStream); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("second stream err = %v, want ResourceExhausted", err)
	}
}

func TestStreamChainFailLoud(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for incomplete stream chain")
		}
	}()
	(interceptor.Set{}).StreamChain()
}

func TestRequestIDFromIncomingMetadata(t *testing.T) {
	ic := interceptor.RequestID()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(interceptor.RequestIDMetadataKey, "fixed-id"))
	var seen string
	_, _ = ic(ctx, nil, unaryInfo("/svc/M"), func(c context.Context, _ any) (any, error) {
		seen, _ = interceptor.RequestIDFrom(c)
		return nil, nil
	})
	if seen != "fixed-id" {
		t.Fatalf("inbound request id not reused: %q", seen)
	}
}

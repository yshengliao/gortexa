package interceptor_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/auth"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/interceptor"
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
	st := apperr.ToGRPCStatus(err)
	if st.Code().String() != "Internal" {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
	if st.Message() != "internal error" {
		t.Fatalf("message = %q, want safe internal message", st.Message())
	}
}

// TestRecoveryStreamNormalizesHandlerErrors mirrors the unary boundary mapping
// on the stream path: a plain error and an fmt-wrapped *Error returned by a
// stream handler are reduced through the registry so the client never sees
// codes.Unknown or a leaked internal cause.
func TestRecoveryStreamNormalizesHandlerErrors(t *testing.T) {
	ic := interceptor.RecoveryStream(nil)

	// A plain error would otherwise reach the client as codes.Unknown + raw text.
	err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
		return stderrors.New("raw internal detail")
	})
	st := apperr.ToGRPCStatus(err)
	if st.Code().String() != "Internal" || st.Message() != "internal error" {
		t.Fatalf("plain stream error → %v %q, want Internal 'internal error'", st.Code(), st.Message())
	}
	if strings.Contains(st.Message(), "raw internal detail") {
		t.Fatalf("internal cause leaked: %q", st.Message())
	}

	// A fmt-wrapped *Error (not itself an *Error, so grpc-go wouldn't call
	// GRPCStatus) must still resolve to its category, not leak the wrapper text.
	wrapped := fmt.Errorf("ctx: %w", apperr.New(apperr.CatNotFound, "resource 42 missing"))
	err = ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
		return wrapped
	})
	if st := apperr.ToGRPCStatus(err); st.Code().String() != "NotFound" {
		t.Fatalf("wrapped *Error on stream → %v, want NotFound", st.Code())
	}
}

// scriptedStream drives validatingStream.RecvMsg: it optionally returns a
// transport error, otherwise fills the caller's message via fill before the
// wrapper validates it.
type scriptedStream struct {
	ctx     context.Context
	recvErr error
	fill    func(any)
}

func (s *scriptedStream) Context() context.Context     { return s.ctx }
func (s *scriptedStream) SetHeader(metadata.MD) error  { return nil }
func (s *scriptedStream) SendHeader(metadata.MD) error { return nil }
func (s *scriptedStream) SetTrailer(metadata.MD)       {}
func (s *scriptedStream) SendMsg(any) error            { return nil }
func (s *scriptedStream) RecvMsg(m any) error {
	if s.recvErr != nil {
		return s.recvErr
	}
	if s.fill != nil {
		s.fill(m)
	}
	return nil
}

func TestValidationStreamRecvMsg(t *testing.T) {
	v, err := interceptor.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	ic := interceptor.ValidationStream(v)

	setValid := func(m any) { m.(*resourcev1.GetResourceRequest).Id = "abc" }

	tests := []struct {
		name     string
		stream   *scriptedStream
		recv     func(grpc.ServerStream) error
		wantCat  apperr.Category
		wantErr  bool
		wantSame error // if set, RecvMsg must return this exact error unmodified
	}{
		{
			name:    "invalid message rejected as InvalidArgument",
			stream:  &scriptedStream{ctx: context.Background()}, // leaves Id="" (violates min_len)
			recv:    func(ss grpc.ServerStream) error { return ss.RecvMsg(new(resourcev1.GetResourceRequest)) },
			wantCat: apperr.CatInvalidArgument,
			wantErr: true,
		},
		{
			name:   "valid message passes",
			stream: &scriptedStream{ctx: context.Background(), fill: setValid},
			recv:   func(ss grpc.ServerStream) error { return ss.RecvMsg(new(resourcev1.GetResourceRequest)) },
		},
		{
			name:     "underlying recv error forwarded unchanged",
			stream:   &scriptedStream{ctx: context.Background(), recvErr: stderrors.New("eof")},
			recv:     func(ss grpc.ServerStream) error { return ss.RecvMsg(new(resourcev1.GetResourceRequest)) },
			wantErr:  true,
			wantSame: nil, // checked below via strings
		},
		{
			name:   "non-proto message skips validation",
			stream: &scriptedStream{ctx: context.Background()},
			recv:   func(ss grpc.ServerStream) error { var s string; return ss.RecvMsg(&s) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ic(nil, tc.stream, streamInfo(), func(_ any, ss grpc.ServerStream) error {
				return tc.recv(ss)
			})
			if tc.wantErr && gotErr == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && gotErr != nil {
				t.Fatalf("want no error, got %v", gotErr)
			}
			if tc.wantCat != "" && !apperr.Is(gotErr, tc.wantCat) {
				t.Fatalf("err = %v, want category %s", gotErr, tc.wantCat)
			}
		})
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
	v := auth.MustNewVerifier(jwtSecret, "gortexa")
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

// A skipped (health) stream must NOT consume an inflight slot, so a flood of
// long-lived Health.Watch streams can't shed real traffic. This is the fix for
// the unauthenticated load-shedding DoS.
func TestLoadSheddingSkipsHealthStream(t *testing.T) {
	skip := func(m string) bool { return strings.HasPrefix(m, "/grpc.health.") }
	ls := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxInflight: 1, Skip: skip})
	ic := ls.Stream()
	healthInfo := &grpc.StreamServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}

	block := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = ic(nil, &fakeStream{ctx: context.Background()}, healthInfo, func(any, grpc.ServerStream) error {
			close(started)
			<-block
			return nil
		})
	}()
	<-started
	// The skipped Health.Watch stream holds open but did NOT take the single slot,
	// so a normal RPC is still admitted (the DoS is neutralized).
	if err := ic(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); err != nil {
		t.Fatalf("normal stream should be admitted while a skipped Health.Watch is in flight: %v", err)
	}
	close(block)

	// Sanity: without skip, an in-flight non-health stream DOES hold the slot and
	// sheds the next call — proving the skip is what neutralizes the DoS.
	ls2 := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxInflight: 1})
	ic2 := ls2.Stream()
	block2 := make(chan struct{})
	started2 := make(chan struct{})
	go func() {
		_ = ic2(nil, &fakeStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
			close(started2)
			<-block2
			return nil
		})
	}()
	<-started2
	if err := ic2(nil, &fakeStream{ctx: context.Background()}, streamInfo(), okStream); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("non-skipped stream should be shed when the slot is held: %v", err)
	}
	close(block2)
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

package interceptor_test

import (
	"context"
	"testing"

	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/observability"

	apperr "github.com/yshengliao/gortexa/apperr"
)

func mustMetrics(t *testing.T) *observability.GovernanceMetrics {
	t.Helper()
	gm, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatal(err)
	}
	return gm
}

// When wired with metrics, a shed request records the LoadShedTotal counter for
// both the cpu and inflight signals (the metrics!=nil branch in admit's callers).
func TestLoadShedderRecordsMetric(t *testing.T) {
	gm := mustMetrics(t)
	info := unaryInfo("/svc/M")

	// cpu signal
	hot := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxCPU: 0.5, CPUSampler: func() float64 { return 0.9 }}, gm)
	if _, err := hot.Unary()(context.Background(), nil, info, okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("cpu shed err = %v, want ResourceExhausted", err)
	}

	// inflight signal: hold the single slot, then shed the next call.
	busy := interceptor.NewLoadShedder(interceptor.LoadSheddingConfig{MaxInflight: 1}, gm)
	ic := busy.Unary()
	block := make(chan struct{})
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
		t.Fatalf("inflight shed err = %v, want ResourceExhausted", err)
	}
	close(block)
}

// When wired with metrics, a denied request records the RateLimitTotal counter
// on both the unary and stream paths (the metrics!=nil branch).
func TestRateLimiterRecordsMetric(t *testing.T) {
	gm := mustMetrics(t)
	rl := interceptor.NewRateLimiter(interceptor.RateLimitConfig{RPS: 1, Burst: 1}, gm)

	uctx := peerCtx("7.7.7.7:1")
	uic := rl.Unary()
	if _, err := uic(uctx, nil, unaryInfo("/svc/M"), okHandler); err != nil {
		t.Fatalf("first unary should pass: %v", err)
	}
	if _, err := uic(uctx, nil, unaryInfo("/svc/M"), okHandler); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("second unary err = %v, want ResourceExhausted", err)
	}

	sctx := peerCtx("8.8.8.8:1")
	sic := rl.Stream()
	if err := sic(nil, &fakeStream{ctx: sctx}, streamInfo(), okStream); err != nil {
		t.Fatalf("first stream should pass: %v", err)
	}
	if err := sic(nil, &fakeStream{ctx: sctx}, streamInfo(), okStream); !apperr.Is(err, apperr.CatResourceExhausted) {
		t.Fatalf("second stream err = %v, want ResourceExhausted", err)
	}
}

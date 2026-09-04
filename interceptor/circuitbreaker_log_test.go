package interceptor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/yshengliao/gortexa/apperr"
)

// syncBuf is a log sink safe for the interceptor's goroutine and the test's.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A breaker opening takes a method dark for every caller, and its only two
// signals were a metric and a span event — both no-ops until an OTLP endpoint
// is configured, which is not the default. So the framework's most
// operationally significant denial produced no output at all out of the box.
// The logger the chain already held simply never reached the breaker.
func TestCircuitBreakerLogsStateChanges(t *testing.T) {
	buf := &syncBuf{}
	cb := NewCircuitBreaker(CBConfig{
		MaxFailures:  2,
		OpenInterval: time.Minute,
		Logger:       slog.New(slog.NewJSONHandler(buf, nil)),
	})

	failing := func(ctx context.Context, req any) (any, error) {
		return nil, apperr.New(apperr.CatInternal, "boom")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}
	for range 2 {
		_, _ = cb.Unary()(context.Background(), nil, info, failing)
	}

	out := buf.String()
	if !strings.Contains(out, "circuit breaker state change") {
		t.Fatalf("opening the breaker must be logged; got:\n%s", out)
	}
	// The line has to name the method and the transition, or it cannot be acted on.
	var rec map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "gortexa: circuit breaker state change" {
			break
		}
	}
	if rec["method"] != "/svc/Method" {
		t.Errorf("log must name the method; got %v", rec["method"])
	}
	if rec["to"] != "open" {
		t.Errorf("log must name the new state; got %v", rec["to"])
	}
	if rec["level"] != "WARN" {
		t.Errorf("a method going dark is a warning; got %v", rec["level"])
	}
}

// A nil Logger keeps the previous behaviour, so an existing caller that builds
// a CBConfig by hand is unaffected.
func TestCircuitBreakerWithoutLoggerStaysSilent(t *testing.T) {
	cb := NewCircuitBreaker(CBConfig{MaxFailures: 1, OpenInterval: time.Minute})
	failing := func(ctx context.Context, req any) (any, error) {
		return nil, apperr.New(apperr.CatInternal, "boom")
	}
	// Must not panic on the nil logger.
	_, _ = cb.Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"}, failing)
}

// NewSet holds a logger and never passed it down; that omission is the whole
// defect, so it is pinned separately from the breaker's own behaviour.
func TestNewSetGivesTheBreakerItsLogger(t *testing.T) {
	buf := &syncBuf{}
	set, err := NewSet(Config{
		Logger:         slog.New(slog.NewJSONHandler(buf, nil)),
		Authenticator:  passAuthenticator{},
		CircuitBreaker: CBConfig{MaxFailures: 1, OpenInterval: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	failing := func(ctx context.Context, req any) (any, error) {
		return nil, apperr.New(apperr.CatInternal, "boom")
	}
	_, _ = set.CircuitBreaker(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"}, failing)

	if !strings.Contains(buf.String(), "circuit breaker state change") {
		t.Fatalf("NewSet must wire Config.Logger into the breaker; got:\n%s", buf.String())
	}
}

// passAuthenticator satisfies NewSet's requirement that the auth stage has a
// driver; the breaker under test sits outside it.
type passAuthenticator struct{}

func (passAuthenticator) Authenticate(ctx context.Context) (context.Context, error) { return ctx, nil }

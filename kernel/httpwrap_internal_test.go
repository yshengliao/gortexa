package kernel

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/health"
)

// WithHTTPWrap used to be a plain assignment, so a second call silently dropped
// the first middleware — a consumer could believe a security wrap was installed
// when it had been discarded. Repeated use now composes, first-supplied
// innermost, and this test pins both halves: that every wrap runs, and the
// order they nest in.
func TestWithHTTPWrapComposesInOrder(t *testing.T) {
	var order []string
	mw := func(tag string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, tag)
				next.ServeHTTP(w, r)
			})
		}
	}
	app, err := New(
		WithLogger(quiet()),
		WithoutInterceptors(),
		WithHTTPWrap(mw("inner")),
		WithHTTPWrap(mw("outer")),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if len(order) != 2 {
		t.Fatalf("both wraps must run; got %v", order)
	}
	if order[0] != "outer" || order[1] != "inner" {
		t.Fatalf("wraps must nest outer-then-inner; got %v", order)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("wrapped handler must still serve /healthz; got %d", rec.Code)
	}
}

// An empty registry makes Overall report Healthy, so /readyz answers 200 with
// nothing probed. That default is deliberate — dependency checks belong to the
// operator — but it should not be silent, because the operator who never read
// the deployment page is the one shipping a service whose readiness is
// meaningless.
func TestServeWarnsWhenNoHealthChecksAreRegistered(t *testing.T) {
	// The logger writes from serve's goroutine while the test reads, so the sink
	// has to be synchronised or -race flags the test itself.
	buf := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(buf, nil))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Addr: ln.Addr().String(), ShutdownTimeout: time.Second}}
	app, err := New(WithConfig(cfg), WithLogger(log), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = app.serve(ctx, ln) }()

	waitFor(t, func() bool { return strings.Contains(buf.String(), "no health checks registered") })
	cancel()
	<-done

	// A registry with a check must not warn.
	buf.Reset()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := &config.Config{Server: config.ServerConfig{Addr: ln2.Addr().String(), ShutdownTimeout: time.Second}}
	app2, err := New(WithConfig(cfg2), WithLogger(log), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	app2.Health().Register("db", func(context.Context) health.State { return health.Healthy })

	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = app2.serve(ctx2, ln2) }()
	waitFor(t, func() bool { return strings.Contains(buf.String(), "gortexa serving") })
	cancel2()
	<-done2

	if strings.Contains(buf.String(), "no health checks registered") {
		t.Fatalf("a registry with a check must not warn; log:\n%s", buf.String())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// syncBuf is a bytes.Buffer safe for one writer goroutine and one reader.
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

func (s *syncBuf) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

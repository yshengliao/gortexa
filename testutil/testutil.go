// Package testutil provides in-process test helpers: a bufconn gRPC server
// wired with the full interceptor chain, golden-file comparison, and goroutine
// leak assertions.
package testutil

import (
	"bytes"
	"context"
	"flag"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/interceptor"
	"github.com/yshengliao/gortexa/internal/observability"
)

const bufSize = 1024 * 1024

// DefaultSecret is the 32-byte HS256 secret used by NewTestServer.
var DefaultSecret = []byte("0123456789abcdef0123456789abcdef")

var update = flag.Bool("update", false, "update golden files")

type tsConfig struct {
	secret []byte
	set    *interceptor.Set
}

// TestServerOption customizes NewTestServer.
type TestServerOption func(*tsConfig)

// WithAuthSecret sets the HS256 secret for the built-in interceptor chain.
func WithAuthSecret(secret []byte) TestServerOption {
	return func(c *tsConfig) { c.secret = secret }
}

// WithInterceptorSet overrides the interceptor chain entirely.
func WithInterceptorSet(s interceptor.Set) TestServerOption {
	return func(c *tsConfig) { c.set = &s }
}

// NewTestServer starts a bufconn gRPC server running the full interceptor chain
// plus the OTel StatsHandler, registers services via register, and returns a
// dialed client connection. Everything is torn down via t.Cleanup.
func NewTestServer(t *testing.T, register func(*grpc.Server), opts ...TestServerOption) *grpc.ClientConn {
	t.Helper()
	cfg := tsConfig{secret: DefaultSecret}
	for _, o := range opts {
		o(&cfg)
	}

	var set interceptor.Set
	if cfg.set != nil {
		set = *cfg.set
	} else {
		s, err := interceptor.NewSet(interceptor.Config{
			Verifier: auth.MustNewVerifier(cfg.secret, "gortexa"),
		})
		if err != nil {
			t.Fatalf("testutil: build interceptors: %v", err)
		}
		set = s
	}

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(set.UnaryChain()...),
		grpc.ChainStreamInterceptor(set.StreamChain()...),
		grpc.StatsHandler(observability.ServerStatsHandler()),
	)
	register(srv)

	lis := bufconn.Listen(bufSize)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("testutil: dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return conn
}

// Golden compares got against testdata/<name>.golden, or rewrites it under -update.
func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run tests with -update to create it)", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// GoleakOptions returns ignores for background goroutines owned by grpc/otel
// that are not real leaks.
func GoleakOptions() []goleak.Option {
	return []goleak.Option{
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/transport.(*controlBuffer).get"),
	}
}

// AssertNoLeak fails the test if leaked goroutines remain.
func AssertNoLeak(t *testing.T) {
	t.Helper()
	goleak.VerifyNone(t, GoleakOptions()...)
}

// VerifyTestMain runs m and asserts no goroutines leaked (use from TestMain).
func VerifyTestMain(m *testing.M) {
	goleak.VerifyTestMain(m, GoleakOptions()...)
}

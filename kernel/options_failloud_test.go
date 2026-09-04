package kernel_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/interceptor"
	"github.com/yshengliao/gortexa/kernel"
)

// The governance chain is what makes the single public port safe to expose, so
// leaving WithInterceptors off must not yield a running server. Before this
// guard existed, kernel.New() returned an App with no recover, request-id,
// load shedding, rate limiting, circuit breaker, auth or validation, and said
// nothing — the chain's own fail-loud panic is unreachable when the Set is
// absent rather than incomplete.
func TestNewRequiresAnInterceptorDecision(t *testing.T) {
	app, err := kernel.New(kernel.WithLogger(quietLogger()))
	if err == nil {
		t.Fatal("New() with no interceptor option must fail; got a usable App")
	}
	if app != nil {
		t.Fatal("New() must not return an App alongside the error")
	}
	// The message has to name both ways out, because the reader hitting it is by
	// definition someone who did not know the choice existed.
	for _, want := range []string{"WithInterceptors", "WithoutInterceptors"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %s; got %q", want, err)
		}
	}
}

// Opting out is legitimate — a test double, an internal-only listener — but it
// has to be a decision the diff shows.
func TestWithoutInterceptorsServesWithNoChain(t *testing.T) {
	app, err := kernel.New(kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors())
	if err != nil {
		t.Fatalf("WithoutInterceptors() must build: %v", err)
	}
	if app.GRPCServer() == nil {
		t.Fatal("WithoutInterceptors() must still build a gRPC server")
	}
}

func TestInterceptorOptionsAreMutuallyExclusive(t *testing.T) {
	// A zero Set is enough: the conflict is decided before any chain is built,
	// so this never reaches the Set's own fail-loud panic.
	_, err := kernel.New(
		kernel.WithLogger(quietLogger()),
		kernel.WithInterceptors(interceptor.Set{}),
		kernel.WithoutInterceptors(),
	)
	if err == nil {
		t.Fatal("supplying both interceptor options must fail")
	}
}

// serve() reads the handler once, so a setter called afterwards was silently
// discarded — and raced that read besides. Both setters now say so.
func TestSettersPanicAfterServing(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*kernel.App)
	}{
		{"SetGateway", func(a *kernel.App) { a.SetGateway(http.NotFoundHandler()) }},
		{"SetMCPHandler", func(a *kernel.App) { a.SetMCPHandler(http.NotFoundHandler()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Server: config.ServerConfig{Addr: ln.Addr().String()}}
			app, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors())
			if err != nil {
				t.Fatal(err)
			}
			_ = ln.Close()

			// Before serving the setter is the documented way to install a handler.
			tc.call(app)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); _ = app.Run(ctx) }()
			waitListening(t, cfg.Server.Addr)

			defer func() {
				cancel()
				<-done
				if r := recover(); r == nil {
					t.Fatalf("%s after serving must panic", tc.name)
				}
			}()
			tc.call(app)
		})
	}
}

// Two admin listeners both write adminAddrV as they bind, so AdminAddr would
// report whichever won the race.
func TestWithAdminListenerRejectsDuplicates(t *testing.T) {
	_, err := kernel.New(
		kernel.WithLogger(quietLogger()),
		kernel.WithoutInterceptors(),
		kernel.WithAdminListener("127.0.0.1:0"),
		kernel.WithAdminListener("127.0.0.1:0"),
	)
	if err == nil {
		t.Fatal("a second WithAdminListener must fail")
	}
	if !strings.Contains(err.Error(), "at most once") {
		t.Errorf("error should say the option is single-use; got %q", err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		t.Fatalf("must fail on the option, not on a bind: %v", err)
	}
}

// waitListening blocks until addr accepts a connection, so the test observes a
// genuinely serving app rather than sleeping and hoping.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	for range 200 {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never began listening on %s", addr)
}

package kernel_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/health"
	"github.com/yshengliao/gortexa/kernel"
)

// runApp starts app.Run in a goroutine and returns a cleanup that cancels and
// waits, so goleak (VerifyTestMain) sees a clean teardown.
func runApp(t *testing.T, app *kernel.App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	return resp.StatusCode
}

// WithExtraListener serves a caller-owned listener + handler under the app
// lifecycle; the caller knows the address immediately.
func TestWithExtraListener(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors(), kernel.WithExtraListener(lis, handler))
	if err != nil {
		t.Fatal(err)
	}
	runApp(t, app)

	url := "http://" + lis.Addr().String() + "/anything"
	// The listener is served from a goroutine; retry briefly until it's up.
	var code int
	for range 50 {
		if req, e := http.NewRequest(http.MethodGet, url, nil); e == nil {
			if resp, e2 := http.DefaultClient.Do(req); e2 == nil {
				code = resp.StatusCode
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusTeapot {
		t.Fatalf("extra listener status = %d, want 418", code)
	}
}

// WithAdminListener serves /healthz and /readyz on a separate port, with readyz
// reflecting the health registry. AdminAddr discovers the :0-bound port.
func TestWithAdminListenerHealth(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors(), kernel.WithAdminListener("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	// An unhealthy check must surface as 503 on /readyz.
	app.Health().Register("dep", func(context.Context) health.State { return health.Unhealthy })
	runApp(t, app)

	var addr net.Addr
	for range 100 {
		if addr = app.AdminAddr(); addr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == nil {
		t.Fatal("AdminAddr never became non-nil")
	}
	base := "http://" + addr.String()

	if code := getStatus(t, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	if code := getStatus(t, base+"/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with an Unhealthy check = %d, want 503", code)
	}
}

// No admin/extra listener → AdminAddr is nil and behaviour is unchanged.
func TestAdminAddrNilWithoutOption(t *testing.T) {
	app := newApp(t, "127.0.0.1:0")
	if app.AdminAddr() != nil {
		t.Fatal("AdminAddr must be nil without WithAdminListener")
	}
}

// A bind failure on the admin listener is fail-loud (Run returns the error) and
// must not leak the already-bound main listener — the main port is re-bindable
// afterwards.
func TestWithAdminListenerBindFailure(t *testing.T) {
	// Pick a currently-free fixed port for the main listener so we can prove it
	// is released after the failed Run.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mainAddr := probe.Addr().String()
	_ = probe.Close()

	cfg := &config.Config{Server: config.ServerConfig{Addr: mainAddr, ShutdownTimeout: 2 * time.Second}}
	app, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors(),
		kernel.WithAdminListener("127.0.0.1:70000")) // invalid port → bind fails
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run must return an error when the admin listener cannot bind")
	}
	// The main listener must have been closed, not leaked.
	l2, err := net.Listen("tcp", mainAddr)
	if err != nil {
		t.Fatalf("main listener leaked (cannot re-bind %s): %v", mainAddr, err)
	}
	_ = l2.Close()
}

// WithExtraListener with a nil listener is a programming error caught at New.
func TestWithExtraListenerNilFailsLoud(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}}
	if _, err := kernel.New(kernel.WithConfig(cfg), kernel.WithLogger(quietLogger()), kernel.WithoutInterceptors(),
		kernel.WithExtraListener(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))); err == nil {
		t.Fatal("New must reject WithExtraListener(nil, ...)")
	}
}

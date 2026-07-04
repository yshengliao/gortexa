package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/test/bufconn"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/health"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

const loopbackBufSize = 1024 * 1024

// App is Gortexa's composition root and lifecycle owner. It holds the gRPC
// server (built with the interceptor chain + StatsHandler), the optional HTTP
// gateway and MCP handlers, a DI container and a health registry, and serves
// them all on one h2c listener.
type App struct {
	cfg          *config.Config
	log          *slog.Logger
	di           *Container
	health       *health.Registry
	grpcSrv      *grpc.Server
	gateway      http.Handler
	mcp          http.Handler
	httpWrap     func(http.Handler) http.Handler
	httpSrv      *http.Server
	shutdownFns  []func(context.Context) error
	shutdownOnce sync.Once
	started      atomic.Bool

	loopbackLis    *bufconn.Listener
	loopbackMu     sync.Mutex
	loopbackConn   *grpc.ClientConn
	loopbackErr    error
	loopbackInit   bool
	loopbackClosed bool
}

// Option configures an App.
type Option func(*appConfig)

type appConfig struct {
	cfg          *config.Config
	log          *slog.Logger
	interceptors *interceptor.Set
	statsHandler stats.Handler
	gateway      http.Handler
	mcp          http.Handler
	httpWrap     func(http.Handler) http.Handler
	shutdownFns  []func(context.Context) error
}

func WithConfig(c *config.Config) Option { return func(a *appConfig) { a.cfg = c } }
func WithLogger(l *slog.Logger) Option   { return func(a *appConfig) { a.log = l } }
func WithInterceptors(s interceptor.Set) Option {
	return func(a *appConfig) { a.interceptors = &s }
}
func WithStatsHandler(h stats.Handler) Option { return func(a *appConfig) { a.statsHandler = h } }
func WithGateway(h http.Handler) Option       { return func(a *appConfig) { a.gateway = h } }
func WithMCPHandler(h http.Handler) Option    { return func(a *appConfig) { a.mcp = h } }
func WithHTTPWrap(mw func(http.Handler) http.Handler) Option {
	return func(a *appConfig) { a.httpWrap = mw }
}
func WithShutdownHook(fn func(context.Context) error) Option {
	return func(a *appConfig) { a.shutdownFns = append(a.shutdownFns, fn) }
}

// New builds an App. If an interceptor Set is provided, its chains are applied
// to the gRPC server (and a missing required interceptor panics — fail-loud).
func New(opts ...Option) (*App, error) {
	ac := &appConfig{}
	for _, o := range opts {
		o(ac)
	}
	if ac.cfg == nil {
		ac.cfg = &config.Config{Server: config.ServerConfig{
			Addr:            ":8080",
			ShutdownTimeout: 20 * time.Second,
			ReadTimeout:     15 * time.Second,
			// WriteTimeout is 0 (disabled): this one server multiplexes long-lived
			// gRPC server-streams (e.g. Health.Watch) and MCP SSE, and a non-zero
			// WriteTimeout is applied per HTTP/2 stream, killing them mid-flight.
			WriteTimeout:      0,
			IdleTimeout:       60 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
		}}
	}
	if ac.log == nil {
		ac.log = slog.Default()
	}

	serverOpts := []grpc.ServerOption{}
	if ac.statsHandler != nil {
		serverOpts = append(serverOpts, grpc.StatsHandler(ac.statsHandler))
	}
	if ac.interceptors != nil {
		serverOpts = append(serverOpts,
			grpc.ChainUnaryInterceptor(ac.interceptors.UnaryChain()...),
			grpc.ChainStreamInterceptor(ac.interceptors.StreamChain()...),
		)
	}

	a := &App{
		cfg:         ac.cfg,
		log:         ac.log,
		di:          NewContainer(),
		health:      health.NewRegistry(),
		gateway:     ac.gateway,
		mcp:         ac.mcp,
		httpWrap:    ac.httpWrap,
		shutdownFns: ac.shutdownFns,
		loopbackLis: bufconn.Listen(loopbackBufSize),
	}
	// Construct httpSrv here (its config is already known) so the field is
	// written once, before any goroutine. serve() only fills in the Handler.
	// This closes the data race with a concurrent, exported Shutdown and means a
	// Shutdown that wins the race still stops the server: http.Server.Shutdown
	// makes a later Serve return ErrServerClosed instead of serving forever.
	a.httpSrv = &http.Server{
		Protocols:         h2cProtocols(),
		ReadTimeout:       ac.cfg.Server.ReadTimeout,
		WriteTimeout:      ac.cfg.Server.WriteTimeout,
		IdleTimeout:       ac.cfg.Server.IdleTimeout,
		ReadHeaderTimeout: ac.cfg.Server.ReadHeaderTimeout,
	}
	a.grpcSrv = grpc.NewServer(serverOpts...)
	grpc_health_v1.RegisterHealthServer(a.grpcSrv, a.health.GRPCHealthServer())
	if ac.cfg.Server.Reflection {
		// Reflection v1 serves live server state, so registering before domain
		// services are added still reflects them. Gated off by default because it
		// exposes the full schema on the shared gRPC/HTTP/MCP port.
		reflection.Register(a.grpcSrv)
	}
	return a, nil
}

// SetGateway installs the HTTP/JSON gateway handler (built after service
// registration, so it is a setter rather than a New option).
func (a *App) SetGateway(h http.Handler) { a.gateway = h }

// SetMCPHandler installs the MCP Streamable HTTP handler.
func (a *App) SetMCPHandler(h http.Handler) { a.mcp = h }

// Loopback returns an in-process client connection to this App's own gRPC
// server. Calls flow through the full interceptor chain, so the gateway and MCP
// bridge inherit auth/validation/etc. The server starts serving the loopback
// listener in Run.
func (a *App) Loopback() (*grpc.ClientConn, error) {
	a.loopbackMu.Lock()
	defer a.loopbackMu.Unlock()
	if a.loopbackClosed {
		// Refuse to create a connection that Shutdown has already passed, which
		// would otherwise leak (Shutdown won't see it).
		return nil, errors.New("kernel: app is shutting down")
	}
	if !a.loopbackInit {
		a.loopbackConn, a.loopbackErr = grpc.NewClient("passthrough:///gortexa-loopback",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return a.loopbackLis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		)
		a.loopbackInit = true
	}
	return a.loopbackConn, a.loopbackErr
}

// Container returns the DI container.
func (a *App) Container() *Container { return a.di }

// GRPCServer returns the gRPC server for service registration.
func (a *App) GRPCServer() *grpc.Server { return a.grpcSrv }

// Health returns the health registry.
func (a *App) Health() *health.Registry { return a.health }

// Logger returns the app logger.
func (a *App) Logger() *slog.Logger { return a.log }

func (a *App) handler() http.Handler {
	mux := newMux(a.grpcSrv, a.mcp, a.gateway,
		http.HandlerFunc(a.handleHealthz),
		http.HandlerFunc(a.handleReadyz),
	)
	if a.httpWrap != nil {
		return a.httpWrap(mux)
	}
	return mux
}

// h2cProtocols enables cleartext HTTP/2 (h2c) alongside HTTP/1.1 on one server,
// using the standard library's native support (Go 1.24+) rather than the
// deprecated golang.org/x/net/http2/h2c wrapper.
func h2cProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

func (a *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Evaluate the checks once: a second pass (Overall + Snapshot) could observe
	// a different state and report a 'status' that contradicts 'checks'.
	snap := a.health.Snapshot(r.Context())
	overall := health.Healthy
	checks := make(map[string]string, len(snap))
	for name, st := range snap {
		if st > overall {
			overall = st
		}
		checks[name] = st.String()
	}
	code := http.StatusOK
	if !overall.Serving() {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": overall.String(), "checks": checks})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// Run starts the h2c listener and blocks until ctx is cancelled or the server
// stops. On cancellation it performs a graceful, bounded shutdown.
func (a *App) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.cfg.Server.Addr)
	if err != nil {
		return err
	}
	return a.serve(ctx, ln)
}

// serve runs against a provided listener (used by Run and by tests).
func (a *App) serve(ctx context.Context, ln net.Listener) error {
	a.started.Store(true)
	// httpSrv was allocated in New(); only its Handler depends on gateway/mcp
	// (installed after New via SetGateway/SetMCPHandler), so set it here. Shutdown
	// never reads Handler, so this is race-free with a concurrent Shutdown.
	a.httpSrv.Handler = a.handler()
	a.log.Info("gortexa serving", "addr", ln.Addr().String())

	// Serve the in-process loopback so gateway/MCP forwarding flows through the
	// interceptor chain.
	go func() { _ = a.grpcSrv.Serve(a.loopbackLis) }()

	errCh := make(chan error, 1)
	go func() { errCh <- a.httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		return a.Shutdown(context.Background())
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		// Serve failed (e.g. a late listener error). Tear down the loopback gRPC
		// server/conn and run the shutdown hooks before returning, so this exit
		// path doesn't leak the loopback goroutine or skip telemetry flush.
		_ = a.Shutdown(context.Background())
		return err
	}
}

// Shutdown gracefully stops the app. It is idempotent and bounded by the
// configured ShutdownTimeout.
func (a *App) Shutdown(ctx context.Context) error {
	var retErr error
	a.shutdownOnce.Do(func() {
		tctx := ctx
		if a.cfg.Server.ShutdownTimeout > 0 {
			var cancel context.CancelFunc
			tctx, cancel = context.WithTimeout(ctx, a.cfg.Server.ShutdownTimeout)
			defer cancel()
		}
		if a.httpSrv != nil {
			if err := a.httpSrv.Shutdown(tctx); err != nil {
				// Graceful shutdown timed out (e.g. a hung MCP SSE stream that
				// never goes idle). Force-close so connections and their
				// goroutines don't leak — mirrors the grpcSrv.Stop fallback below.
				_ = a.httpSrv.Close()
				retErr = err
			}
		}
		// Close the loopback under the same lock that guards its creation, and
		// mark it closed so a concurrent Loopback() can't create a conn we miss.
		a.loopbackMu.Lock()
		a.loopbackClosed = true
		loopbackConn := a.loopbackConn
		a.loopbackMu.Unlock()
		if loopbackConn != nil {
			_ = loopbackConn.Close()
		}
		if a.grpcSrv != nil {
			// Drain in-flight RPCs gracefully, but fall back to a hard stop if
			// the bounded shutdown deadline expires.
			stopped := make(chan struct{})
			go func() {
				a.grpcSrv.GracefulStop()
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-tctx.Done():
				a.grpcSrv.Stop()
			}
		}
		if a.loopbackLis != nil {
			_ = a.loopbackLis.Close()
		}
		// Run shutdown hooks (OTel trace/metric/log flush) with their own bounded
		// budget derived from the caller's ctx, not tctx — which the HTTP/gRPC
		// drain above may have already exhausted, leaving the flush no time.
		hookCtx := ctx
		if a.cfg.Server.ShutdownTimeout > 0 {
			var hookCancel context.CancelFunc
			hookCtx, hookCancel = context.WithTimeout(ctx, a.cfg.Server.ShutdownTimeout)
			defer hookCancel()
		}
		for _, fn := range a.shutdownFns {
			if err := fn(hookCtx); err != nil && retErr == nil {
				retErr = err
			}
		}
	})
	return retErr
}

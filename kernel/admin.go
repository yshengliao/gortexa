package kernel

import (
	"net"
	"net/http"
	"time"
)

// Timeouts for secondary listeners. ReadHeaderTimeout (slow-loris guard) and
// IdleTimeout apply to every secondary server, including a consumer's own
// WithExtraListener handler. The built-in admin health server additionally
// takes full read/write deadlines because it only serves short bodies.
const (
	adminReadHeaderTimeout = 5 * time.Second
	adminIdleTimeout       = 60 * time.Second
	adminReadWriteTimeout  = 10 * time.Second
)

// extraListenerSpec is a secondary listener requested via an Option, resolved
// into an extraListener in New. Exactly one of lis (caller-owned) or addr
// (framework-bound in serve) is set.
type extraListenerSpec struct {
	lis     net.Listener
	handler http.Handler
	addr    string
	admin   bool // built-in admin health listener (drives AdminAddr)
}

// extraListener is a secondary HTTP server the App serves alongside the main
// h2c port and tears down with it.
type extraListener struct {
	srv   *http.Server
	lis   net.Listener // caller-provided; nil means bind addr in serve
	addr  string       // bind target when lis is nil
	admin bool
}

// WithExtraListener serves h on lis alongside the main port, under the same
// graceful-shutdown lifecycle. Use it for an ops surface (pprof, a custom
// metrics or health handler) on a separate — e.g. internal-only — listener. The
// main single h2c port is unchanged; this is purely additive and opt-in.
func WithExtraListener(lis net.Listener, h http.Handler) Option {
	return func(a *appConfig) {
		a.extraListeners = append(a.extraListeners, extraListenerSpec{lis: lis, handler: h})
	}
}

// WithAdminListener binds addr as a separate plain-HTTP/1.1 ops port serving
// only /healthz and /readyz, so liveness/readiness probes can target a private
// port distinct from the public gRPC/HTTP/MCP port. The main h2c port (which
// also serves these endpoints) is unchanged. Opt-in; if the bind fails, Run
// returns the error. Use AdminAddr to discover the bound port (e.g. with :0).
// Repeated use is rejected by New: two admin listeners both write adminAddrV as
// they bind, so AdminAddr would report whichever won the race.
func WithAdminListener(addr string) Option {
	return func(a *appConfig) {
		a.extraListeners = append(a.extraListeners, extraListenerSpec{addr: addr, admin: true})
	}
}

// adminHealthHandler serves just the ops health endpoints on a separate port:
// GET /healthz (liveness, always 200) and GET /readyz (readiness, 200/503),
// reusing the App's handlers. No gRPC, MCP or gateway is exposed here.
func (a *App) adminHealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/readyz", a.handleReadyz)
	return mux
}

// AdminAddr returns the resolved address of the admin listener once serving has
// started, or nil if there is no admin listener or Run has not bound it yet.
func (a *App) AdminAddr() net.Addr {
	a.adminAddrMu.Lock()
	defer a.adminAddrMu.Unlock()
	return a.adminAddrV
}

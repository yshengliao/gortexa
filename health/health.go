// Package health provides a concurrency-safe three-state health registry
// (Healthy / Degraded / Unhealthy) and a bridge to the standard gRPC health
// protocol. Degraded is still considered serving; only Unhealthy is not.
package health

import (
	"context"
	"maps"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/observability"
)

// State is a component's health.
type State int

const (
	Healthy State = iota
	Degraded
	Unhealthy
)

func (s State) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	case Unhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// Serving reports whether the state should accept traffic.
func (s State) Serving() bool { return s != Unhealthy }

// Check reports the current state of one component.
type Check func(ctx context.Context) State

// checkTimeout bounds a single check invocation. A check is expected to be
// fast; without a ceiling a blocking check (a DB/Redis ping with no internal
// deadline) would stall the serving Check RPC or a long-lived Watch stream for
// as long as the caller's own deadline allows. The metrics exporter already
// bounds its whole snapshot the same way — this applies it per check, so one
// slow check can't hold up the others. The check must honour the ctx it is
// given for the ceiling to take effect.
const checkTimeout = 5 * time.Second

// evalCheck runs one check under a bounded context so a hung check can't stall
// the caller.
func evalCheck(ctx context.Context, c Check) State {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return c(ctx)
}

// Registry holds named checks and aggregates them.
type Registry struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{checks: make(map[string]Check)} }

// Register adds or replaces a named check.
func (r *Registry) Register(name string, c Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = c
}

// Snapshot evaluates every check.
func (r *Registry) Snapshot(ctx context.Context) map[string]State {
	r.mu.RLock()
	checks := make(map[string]Check, len(r.checks))
	maps.Copy(checks, r.checks)
	r.mu.RUnlock()

	out := make(map[string]State, len(checks))
	for n, c := range checks {
		out[n] = evalCheck(ctx, c)
	}
	return out
}

// Overall returns the worst state across all checks. An empty registry is Healthy.
func (r *Registry) Overall(ctx context.Context) State {
	worst := Healthy
	for _, s := range r.Snapshot(ctx) {
		if s > worst {
			worst = s
		}
	}
	return worst
}

// State evaluates a single named check. ok is false when no such check is
// registered (used to answer the gRPC health protocol's per-service queries).
func (r *Registry) State(ctx context.Context, name string) (state State, ok bool) {
	r.mu.RLock()
	c, ok := r.checks[name]
	r.mu.RUnlock()
	if !ok {
		return Healthy, false
	}
	return evalCheck(ctx, c), true
}

// Names returns the registered check names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.checks))
	for n := range r.checks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// GRPCHealthServer returns an adapter implementing grpc.health.v1.Health.
func (r *Registry) GRPCHealthServer() grpc_health_v1.HealthServer {
	return &grpcHealth{reg: r}
}

type grpcHealth struct {
	grpc_health_v1.UnimplementedHealthServer
	reg *Registry
}

func serving(s State) grpc_health_v1.HealthCheckResponse_ServingStatus {
	if s.Serving() {
		return grpc_health_v1.HealthCheckResponse_SERVING
	}
	return grpc_health_v1.HealthCheckResponse_NOT_SERVING
}

// statusFor resolves the serving status for a health request's service field: an
// empty service is the overall server health; a non-empty service names a
// registered check. found is false for an unknown non-empty service.
func (g *grpcHealth) statusFor(ctx context.Context, service string) (status grpc_health_v1.HealthCheckResponse_ServingStatus, found bool) {
	if service == "" {
		return serving(g.reg.Overall(ctx)), true
	}
	st, ok := g.reg.State(ctx, service)
	if !ok {
		return grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN, false
	}
	return serving(st), true
}

func (g *grpcHealth) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	st, found := g.statusFor(ctx, req.GetService())
	if !found {
		return nil, status.Error(codes.NotFound, "unknown health service")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: st}, nil
}

// watchInterval is how often Watch re-evaluates health to detect a change.
const watchInterval = time.Second

func (g *grpcHealth) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	service := req.GetService()
	// -1 is an impossible ServingStatus, so the first evaluation always sends.
	last := grpc_health_v1.HealthCheckResponse_ServingStatus(-1)
	send := func() error {
		st, _ := g.statusFor(stream.Context(), service) // SERVICE_UNKNOWN when absent
		if st == last {
			return nil
		}
		last = st
		return stream.Send(&grpc_health_v1.HealthCheckResponse{Status: st})
	}
	if err := send(); err != nil {
		return err
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

// StartMetricsExport records component health states every interval until ctx is
// cancelled. The goroutine exits on ctx.Done so it never outlives the server
// (goleak-safe); pass interval <= 0 for the 15s default.
func (r *Registry) StartMetricsExport(ctx context.Context, metrics *observability.GovernanceMetrics, interval time.Duration) {
	if metrics == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Bound each evaluation so a hung check can't stall the exporter.
				evalCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				snapshot := r.Snapshot(evalCtx)
				cancel()
				for component, state := range snapshot {
					// The state belongs in the value, never in an attribute: under
					// cumulative temporality the SDK re-exports every attribute set it
					// has ever seen, so a "state" label would leave one frozen series
					// per state a component ever reached and latch alerts forever.
					// One series per component keeps the last value current.
					metrics.HealthStateGauge.Record(ctx, int64(state), metric.WithAttributes(attribute.String("component", component)))
				}
			}
		}
	}()
}

// Package health provides a concurrency-safe three-state health registry
// (Healthy / Degraded / Unhealthy) and a bridge to the standard gRPC health
// protocol. Degraded is still considered serving; only Unhealthy is not.
package health

import (
	"context"
	"sort"
	"sync"

	"google.golang.org/grpc/health/grpc_health_v1"
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
	for n, c := range r.checks {
		checks[n] = c
	}
	r.mu.RUnlock()

	out := make(map[string]State, len(checks))
	for n, c := range checks {
		out[n] = c(ctx)
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

func (g *grpcHealth) status(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
	if g.reg.Overall(ctx).Serving() {
		return grpc_health_v1.HealthCheckResponse_SERVING
	}
	return grpc_health_v1.HealthCheckResponse_NOT_SERVING
}

func (g *grpcHealth) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return &grpc_health_v1.HealthCheckResponse{Status: g.status(ctx)}, nil
}

func (g *grpcHealth) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	// Minimal watch: emit the current status, then hold until the client leaves.
	if err := stream.Send(&grpc_health_v1.HealthCheckResponse{Status: g.status(stream.Context())}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

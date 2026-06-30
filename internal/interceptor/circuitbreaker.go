package interceptor

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/observability"
)

// CBConfig configures the per-method circuit breaker. MaxFailures <= 0 disables it.
type CBConfig struct {
	MaxFailures  int           // consecutive failures before opening
	OpenInterval time.Duration // how long to stay open before probing
	HalfOpenMax  int           // concurrent probes allowed while half-open
}

type cbState int

const (
	cbClosed cbState = iota
	cbOpen
	cbHalfOpen
)

type breaker struct {
	mu          sync.Mutex
	maxFailures int
	openFor     time.Duration
	halfOpenMax int

	state    cbState
	failures int
	openedAt time.Time
	probes   int
}

// allow returns whether the request may proceed, plus state-change details for
// the Open→HalfOpen probe transition so the caller can record the metric/span.
func (b *breaker) allow() (ok bool, from, to cbState, changed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := b.state
	switch b.state {
	case cbOpen:
		if time.Since(b.openedAt) >= b.openFor {
			b.state = cbHalfOpen
			b.probes = 1
			return true, prev, b.state, true
		}
		return false, prev, b.state, false
	case cbHalfOpen:
		if b.probes < b.halfOpenMax {
			b.probes++
			return true, prev, b.state, false
		}
		return false, prev, b.state, false
	default: // cbClosed
		return true, prev, b.state, false
	}
}

func (b *breaker) record(success bool) (cbState, cbState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := b.state
	switch b.state {
	case cbHalfOpen:
		if b.probes > 0 {
			b.probes--
		}
		if success {
			b.state = cbClosed
			b.failures = 0
		} else {
			b.state = cbOpen
			b.openedAt = time.Now()
		}
	case cbClosed:
		if success {
			b.failures = 0
			return old, b.state, old != b.state
		}
		b.failures++
		if b.failures >= b.maxFailures {
			b.state = cbOpen
			b.openedAt = time.Now()
		}
	case cbOpen:
		// A probe result that arrives after the breaker has already re-opened
		// (another concurrent half-open probe failed first) is intentionally
		// dropped: the open timer governs the next probe window.
	}
	return old, b.state, old != b.state
}

func (s cbState) String() string {
	switch s {
	case cbClosed:
		return "closed"
	case cbOpen:
		return "open"
	case cbHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitBreaker trips per method after repeated server-side failures.
type CircuitBreaker struct {
	cfg      CBConfig
	mu       sync.Mutex
	breakers map[string]*breaker
	metrics  *observability.GovernanceMetrics
}

// NewCircuitBreaker builds a CircuitBreaker from config.
func NewCircuitBreaker(cfg CBConfig, metrics ...*observability.GovernanceMetrics) *CircuitBreaker {
	if cfg.HalfOpenMax <= 0 {
		cfg.HalfOpenMax = 1
	}
	if cfg.OpenInterval <= 0 {
		cfg.OpenInterval = 5 * time.Second
	}
	var gm *observability.GovernanceMetrics
	if len(metrics) > 0 {
		gm = metrics[0]
	}
	return &CircuitBreaker{cfg: cfg, breakers: make(map[string]*breaker), metrics: gm}
}

func (c *CircuitBreaker) enabled() bool { return c.cfg.MaxFailures > 0 }

func (c *CircuitBreaker) get(method string) *breaker {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.breakers[method]
	if b == nil {
		b = &breaker{maxFailures: c.cfg.MaxFailures, openFor: c.cfg.OpenInterval, halfOpenMax: c.cfg.HalfOpenMax}
		c.breakers[method] = b
	}
	return b
}

// isFailure counts only server-side failures toward tripping the breaker.
func isFailure(err error) bool {
	if err == nil {
		return false
	}
	switch apperr.ToGRPCStatus(err).Code() {
	case codes.Unavailable, codes.Internal, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// Unary returns the unary interceptor.
func (c *CircuitBreaker) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !c.enabled() {
			return handler(ctx, req)
		}
		b := c.get(info.FullMethod)
		ok, from, to, changed := b.allow()
		if !ok {
			return nil, apperr.New(apperr.CatUnavailable, "circuit open")
		}
		c.recordChange(ctx, info.FullMethod, from, to, changed)
		// Record the outcome in a defer so a panicking handler still counts. A
		// panic unwinds past this frame to the outer Recovery interceptor, so a
		// non-deferred record would be skipped entirely: the breaker would never
		// see panic-induced failures (which map to Internal, a tripping category),
		// and a half-open probe would never release its slot — permanently wedging
		// the method open. Treat a panic as a failure; the panic keeps propagating
		// after the defer runs, so Recovery still converts it to an Internal error.
		success := false
		defer func() {
			from, to, changed := b.record(success)
			c.recordChange(ctx, info.FullMethod, from, to, changed)
		}()
		resp, err := handler(ctx, req)
		success = !isFailure(err)
		return resp, err
	}
}

// Stream returns the stream interceptor.
func (c *CircuitBreaker) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !c.enabled() {
			return handler(srv, ss)
		}
		b := c.get(info.FullMethod)
		ok, from, to, changed := b.allow()
		if !ok {
			return apperr.New(apperr.CatUnavailable, "circuit open")
		}
		c.recordChange(ss.Context(), info.FullMethod, from, to, changed)
		// See Unary: record in a defer so a panicking handler still counts and a
		// half-open probe slot is always released.
		success := false
		defer func() {
			from, to, changed := b.record(success)
			c.recordChange(ss.Context(), info.FullMethod, from, to, changed)
		}()
		err := handler(srv, ss)
		success = !isFailure(err)
		return err
	}
}

func (c *CircuitBreaker) recordChange(ctx context.Context, method string, from cbState, to cbState, changed bool) {
	if !changed {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("method", method), attribute.String("from", from.String()), attribute.String("to", to.String())}
	if c.metrics != nil {
		c.metrics.CBStateChanges.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	trace.SpanFromContext(ctx).AddEvent("cb.state_change", trace.WithAttributes(attrs...))
}

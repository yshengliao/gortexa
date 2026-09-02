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

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/observability"
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
	// halfOpenedAt stamps when the current half-open episode began, so allow can
	// abandon an episode whose probes never come back (see the cbHalfOpen branch).
	halfOpenedAt time.Time
	probes       int
	// gen identifies the current state episode; it is bumped on every transition
	// so a probe's outcome only resolves the half-open episode it was actually
	// admitted into (see record).
	gen uint64
}

// admission carries whether a call may proceed and, when it was admitted as a
// half-open probe, the episode (gen) it belongs to — plus state-change details
// for the Open→HalfOpen transition so the caller can record the metric/span.
type admission struct {
	ok      bool
	probe   bool
	gen     uint64
	from    cbState
	to      cbState
	changed bool
}

// allow reports whether the request may proceed.
func (b *breaker) allow() admission {
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := b.state
	switch b.state {
	case cbOpen:
		if time.Since(b.openedAt) >= b.openFor {
			b.state = cbHalfOpen
			b.gen++
			b.probes = 1
			b.halfOpenedAt = time.Now()
			return admission{ok: true, probe: true, gen: b.gen, from: prev, to: b.state, changed: true}
		}
		return admission{ok: false, from: prev, to: b.state}
	case cbHalfOpen:
		if b.probes < b.halfOpenMax {
			b.probes++
			return admission{ok: true, probe: true, gen: b.gen, from: prev, to: b.state}
		}
		// Probe slots are released only by record, which never runs for a handler
		// that never returns — a long-lived server stream, or a call blocked on a
		// dependency with no deadline (the h2c server sets no read/write timeout).
		// Unlike cbOpen, half-open had no elapsed-time escape, so such probes wedged
		// the method shut for every other caller. Bound the episode by openFor:
		// abandon it and start a fresh one with this caller as its first probe. The
		// stuck probes' eventual results are dropped by record's generation guard.
		if time.Since(b.halfOpenedAt) >= b.openFor {
			b.gen++
			b.probes = 1
			b.halfOpenedAt = time.Now()
			return admission{ok: true, probe: true, gen: b.gen, from: prev, to: b.state}
		}
		return admission{ok: false, from: prev, to: b.state}
	default: // cbClosed
		return admission{ok: true, from: prev, to: b.state}
	}
}

// record folds a completed call's outcome back into the breaker. adm is the
// admission allow returned for this same call, so a stale request admitted in an
// earlier episode can't steal a probe slot or flip state on the genuine probe's
// behalf when it completes during a later half-open episode.
func (b *breaker) record(ctx context.Context, method string, out outcome, adm admission, c *CircuitBreaker) {
	b.mu.Lock()
	old := b.state
	switch b.state {
	case cbHalfOpen:
		// Only a probe admitted into the current half-open episode may resolve it.
		if !adm.probe || adm.gen != b.gen {
			break
		}
		if b.probes > 0 {
			b.probes--
		}
		// A neutral outcome still frees the slot — the probe is over — but it is no
		// evidence either way, so the episode stays open for a real probe instead of
		// being healed (or re-opened) by a client-caused rejection.
		if out == outcomeNeutral {
			break
		}
		if out == outcomeSuccess {
			b.state = cbClosed
			b.failures = 0
		} else {
			b.state = cbOpen
			b.openedAt = time.Now()
		}
		b.gen++
	case cbClosed:
		// A half-open probe that lands after the breaker already closed must not
		// be counted as a normal closed-state failure.
		if adm.probe {
			break
		}
		// Neutral outcomes neither trip nor reset: otherwise a trickle of auth or
		// validation rejections interleaved into a real outage would keep clearing
		// the consecutive-failure counter and the breaker would never open.
		if out == outcomeNeutral {
			break
		}
		if out == outcomeSuccess {
			b.failures = 0
			break
		}
		b.failures++
		if b.failures >= b.maxFailures {
			b.state = cbOpen
			b.openedAt = time.Now()
			b.gen++
		}
	case cbOpen:
		// A probe result that arrives after the breaker has already re-opened
		// (another concurrent half-open probe failed first) is intentionally
		// dropped: the open timer governs the next probe window.
	}

	newState := b.state
	changed := old != newState
	b.mu.Unlock() // Unlock before potentially slow/blocking metric emission

	if changed {
		c.recordChange(ctx, method, old, newState)
	}
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

// outcome is a call's verdict for the breaker. It is three-valued because the
// breaker sits outside auth and validation in the fixed chain and therefore sees
// outcomes the server is not responsible for: those must be neutral, counting
// neither as a failure (which would trip a healthy method) nor as a success
// (which would reset the failure counter or heal a half-open episode over an
// ongoing outage). The zero value is outcomeFailure so a panicking handler,
// which unwinds before the verdict is assigned, still counts against the method.
type outcome int

const (
	outcomeFailure outcome = iota
	outcomeSuccess
	outcomeNeutral
)

// classify decides what a completed call says about server health. Only genuine
// server-side failures trip the breaker; client-caused outcomes are neutral.
func classify(ctx context.Context, err error) outcome {
	// The caller walked away (cancelled, or its deadline expired) — whatever the
	// handler returned is about the client, not this method's health. This also
	// covers a bare context.Canceled, which apperr launders into Internal (a
	// tripping category) because status.FromError does not recognise it.
	if ctx.Err() != nil {
		return outcomeNeutral
	}
	if err == nil {
		return outcomeSuccess
	}
	switch apperr.ToGRPCStatus(err).Code() {
	case codes.Unavailable, codes.Internal, codes.DeadlineExceeded:
		return outcomeFailure
	case codes.Canceled, codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied:
		// Client fault — and the two stages inside the breaker (auth, validation)
		// reject here without ever reaching the handler, so there is nothing to
		// learn about the downstream from them.
		return outcomeNeutral
	default:
		return outcomeSuccess
	}
}

// Unary returns the unary interceptor.
func (c *CircuitBreaker) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !c.enabled() {
			return handler(ctx, req)
		}
		b := c.get(info.FullMethod)
		adm := b.allow()
		if !adm.ok {
			return nil, apperr.New(apperr.CatUnavailable, "circuit open")
		}
		if adm.changed {
			c.recordChange(ctx, info.FullMethod, adm.from, adm.to)
		}
		// Record the outcome in a defer so a panicking handler still counts. A
		// panic unwinds past this frame to the outer Recovery interceptor, so a
		// non-deferred record would be skipped entirely: the breaker would never
		// see panic-induced failures (which map to Internal, a tripping category),
		// and a half-open probe would never release its slot — permanently wedging
		// the method open. Treat a panic as a failure; the panic keeps propagating
		// after the defer runs, so Recovery still converts it to an Internal error.
		out := outcomeFailure
		defer func() {
			b.record(ctx, info.FullMethod, out, adm, c)
		}()
		resp, err := handler(ctx, req)
		out = classify(ctx, err)
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
		adm := b.allow()
		if !adm.ok {
			return apperr.New(apperr.CatUnavailable, "circuit open")
		}
		if adm.changed {
			c.recordChange(ss.Context(), info.FullMethod, adm.from, adm.to)
		}
		// See Unary: record in a defer so a panicking handler still counts and a
		// half-open probe slot is always released.
		out := outcomeFailure
		defer func() {
			b.record(ss.Context(), info.FullMethod, out, adm, c)
		}()
		err := handler(srv, ss)
		out = classify(ss.Context(), err)
		return err
	}
}

func (c *CircuitBreaker) recordChange(ctx context.Context, method string, from cbState, to cbState) {
	attrs := []attribute.KeyValue{attribute.String("method", method), attribute.String("from", from.String()), attribute.String("to", to.String())}
	if c.metrics != nil {
		c.metrics.CBStateChanges.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	trace.SpanFromContext(ctx).AddEvent("cb.state_change", trace.WithAttributes(attrs...))
}

package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// GovernanceMetrics exposes counters/gauges for control-plane decisions.
type GovernanceMetrics struct {
	LoadShedTotal    metric.Int64Counter
	RateLimitTotal   metric.Int64Counter
	CBStateChanges   metric.Int64Counter
	AuthDenied       metric.Int64Counter
	ValidationFails  metric.Int64Counter
	HealthStateGauge metric.Int64Gauge
}

// NewGovernanceMetrics creates OTel instruments in the gortexa scope.
func NewGovernanceMetrics() (*GovernanceMetrics, error) {
	m := otel.GetMeterProvider().Meter("gortexa")
	load, err := m.Int64Counter("gortexa_load_shed_total")
	if err != nil {
		return nil, err
	}
	rl, err := m.Int64Counter("gortexa_rate_limit_total")
	if err != nil {
		return nil, err
	}
	cb, err := m.Int64Counter("gortexa_cb_state_changes_total")
	if err != nil {
		return nil, err
	}
	auth, err := m.Int64Counter("gortexa_auth_denied_total")
	if err != nil {
		return nil, err
	}
	val, err := m.Int64Counter("gortexa_validation_fails_total")
	if err != nil {
		return nil, err
	}
	health, err := m.Int64Gauge("gortexa_health_state")
	if err != nil {
		return nil, err
	}
	return &GovernanceMetrics{load, rl, cb, auth, val, health}, nil
}

func (g *GovernanceMetrics) RecordHealth(ctx context.Context, component string, state int64) {
	if g != nil {
		g.HealthStateGauge.Record(ctx, state, metric.WithAttributes())
	}
}

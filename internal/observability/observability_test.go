package observability_test

import (
	"context"
	"testing"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/observability"
)

func TestNewLogger(t *testing.T) {
	if observability.NewLogger(config.LogConfig{Level: "debug", Format: "json"}) == nil {
		t.Fatal("nil logger")
	}
	if observability.NewLogger(config.LogConfig{Format: "text"}) == nil {
		t.Fatal("nil text logger")
	}
}

func TestServerStatsHandler(t *testing.T) {
	if observability.ServerStatsHandler() == nil {
		t.Fatal("nil stats handler")
	}
}

func TestSetupNoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	st, err := observability.SetupTracing(ctx, config.ObservConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st(ctx); err != nil {
		t.Fatal(err)
	}
	sm, err := observability.SetupMetrics(ctx, config.ObservConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sm(ctx); err != nil {
		t.Fatal(err)
	}
}

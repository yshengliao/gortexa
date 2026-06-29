package storage_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/storage"
)

func TestBuildPoolConfigPgBouncerSafe(t *testing.T) {
	cfg := config.DBConfig{DSN: config.Secret("postgres://u:p@localhost:5432/db"), MaxConns: 7}
	pc, err := storage.BuildPoolConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pc.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Errorf("exec mode = %v, want QueryExecModeExec (PgBouncer-safe)", pc.ConnConfig.DefaultQueryExecMode)
	}
	if pc.ConnConfig.StatementCacheCapacity != 0 || pc.ConnConfig.DescriptionCacheCapacity != 0 {
		t.Errorf("caches not disabled: stmt=%d desc=%d", pc.ConnConfig.StatementCacheCapacity, pc.ConnConfig.DescriptionCacheCapacity)
	}
	if pc.MaxConns != 7 {
		t.Errorf("max conns = %d, want 7", pc.MaxConns)
	}
}

func TestBuildPoolConfigBadDSN(t *testing.T) {
	if _, err := storage.BuildPoolConfig(config.DBConfig{DSN: config.Secret("::not a dsn::")}, nil); err == nil {
		t.Fatal("expected error for invalid dsn")
	}
}

func TestDBTracerEmitsSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	tr := storage.NewDBTracer(tp)

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	spans := sr.Ended()
	if len(spans) != 1 || spans[0].Name() != "db.query" {
		t.Fatalf("spans = %d, want 1 db.query", len(spans))
	}
	var found bool
	for _, a := range spans[0].Attributes() {
		if a.Key == "db.statement" && a.Value.AsString() == "SELECT 1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("db.statement attribute missing: %v", spans[0].Attributes())
	}
}

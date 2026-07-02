package storage_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/jackc/pgx/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/storage"
)

// fakeTracer is a non-DBTracer pgx.QueryTracer used to exercise the plain
// tracer-wiring branch of BuildPoolConfig.
type fakeTracer struct{}

func (fakeTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (fakeTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestBuildPoolConfigTracerWiring(t *testing.T) {
	cfg := config.DBConfig{DSN: config.Secret("postgres://u:p@db.example.com:5432/app")}

	cases := []struct {
		name   string
		tracer pgx.QueryTracer
		check  func(t *testing.T, got pgx.QueryTracer)
	}{
		{
			name:   "nil tracer leaves conn tracer unset",
			tracer: nil,
			check: func(t *testing.T, got pgx.QueryTracer) {
				if got != nil {
					t.Errorf("tracer = %v, want nil", got)
				}
			},
		},
		{
			name:   "plain QueryTracer wired as-is",
			tracer: fakeTracer{},
			check: func(t *testing.T, got pgx.QueryTracer) {
				if _, ok := got.(fakeTracer); !ok {
					t.Errorf("tracer = %T, want fakeTracer", got)
				}
			},
		},
		{
			name:   "DBTracer specialized with server address",
			tracer: storage.NewDBTracer(nil),
			check: func(t *testing.T, got pgx.QueryTracer) {
				if _, ok := got.(*storage.DBTracer); !ok {
					t.Errorf("tracer = %T, want *storage.DBTracer", got)
				}
			},
		},
		{
			name:   "typed-nil DBTracer does not panic",
			tracer: (*storage.DBTracer)(nil),
			check:  func(t *testing.T, got pgx.QueryTracer) {},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pc, err := storage.BuildPoolConfig(cfg, c.tracer)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, pc.ConnConfig.Tracer)
		})
	}
}

func TestBuildPoolConfigDefaultMaxConns(t *testing.T) {
	cfg := config.DBConfig{DSN: config.Secret("postgres://u:p@localhost:5432/db")} // MaxConns unset
	pc, err := storage.BuildPoolConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pc.MaxConns <= 0 {
		t.Errorf("default MaxConns = %d, want pgxpool default > 0", pc.MaxConns)
	}
}

func TestBuildPoolConfigBadDSNCategory(t *testing.T) {
	_, err := storage.BuildPoolConfig(config.DBConfig{DSN: config.Secret("::not a dsn::")}, nil)
	var e *apperr.Error
	if !stderrors.As(err, &e) || e.Category != apperr.CatInvalidArgument {
		t.Fatalf("err = %v, want *errors.Error with CatInvalidArgument", err)
	}
}

func TestDBTracerRecordsServerAddress(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	cfg := config.DBConfig{DSN: config.Secret("postgres://u:p@db.example.com:5432/app")}
	pc, err := storage.BuildPoolConfig(cfg, storage.NewDBTracer(tp))
	if err != nil {
		t.Fatal(err)
	}
	tr := pc.ConnConfig.Tracer
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	var addr string
	for _, a := range spans[0].Attributes() {
		if a.Key == "server.address" {
			addr = a.Value.AsString()
		}
	}
	if addr != "db.example.com" {
		t.Errorf("server.address = %q, want %q", addr, "db.example.com")
	}
}

func TestDBTracerQueryEndBranches(t *testing.T) {
	t.Run("error recorded on span", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		tr := storage.NewDBTracer(tp)

		ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: stderrors.New("boom")})

		spans := sr.Ended()
		if len(spans) != 1 {
			t.Fatalf("spans = %d, want 1", len(spans))
		}
		if spans[0].Status().Code.String() != "Error" {
			t.Errorf("status = %v, want Error", spans[0].Status().Code)
		}
		if len(spans[0].Events()) == 0 {
			t.Error("expected a recorded error event on the span")
		}
	})

	t.Run("no span in ctx is a no-op", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		tr := storage.NewDBTracer(tp)

		// Must not panic and must not end any span.
		tr.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
		if n := len(sr.Ended()); n != 0 {
			t.Errorf("spans = %d, want 0", n)
		}
	})
}

func TestDBTracerNilReceiverWithServerAddress(t *testing.T) {
	var tr *storage.DBTracer
	if got := tr.WithServerAddress("db.example.com"); got != nil {
		t.Errorf("nil receiver WithServerAddress = %v, want nil", got)
	}
}

func TestNewPool(t *testing.T) {
	t.Run("valid dsn builds lazily without a server", func(t *testing.T) {
		cfg := config.DBConfig{DSN: config.Secret("postgres://u:p@localhost:5432/db"), MaxConns: 2}
		pool, err := storage.NewPool(context.Background(), cfg, storage.NewDBTracer(nil))
		if err != nil {
			t.Fatal(err)
		}
		if pool == nil {
			t.Fatal("pool is nil")
		}
		pool.Close()
	})

	t.Run("invalid dsn propagates invalid_argument", func(t *testing.T) {
		_, err := storage.NewPool(context.Background(), config.DBConfig{DSN: config.Secret("::not a dsn::")}, nil)
		var e *apperr.Error
		if !stderrors.As(err, &e) || e.Category != apperr.CatInvalidArgument {
			t.Fatalf("err = %v, want *errors.Error with CatInvalidArgument", err)
		}
	})
}

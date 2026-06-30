package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// DBTracer implements pgx.QueryTracer, emitting one OTel span per query with the
// statement and command tag. It is the seam for N+1 detection (db.statement
// count) and transaction-duration monitoring.
type DBTracer struct {
	tracer        trace.Tracer
	serverAddress string
}

// NewDBTracer builds a DBTracer from a tracer provider (global if nil).
func NewDBTracer(tp trace.TracerProvider) *DBTracer {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &DBTracer{tracer: tp.Tracer("github.com/yshengliao/gortexa/internal/storage")}
}

type dbSpanKey struct{}

// TraceQueryStart begins a db.query span.
func (t *DBTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "db.query")
	attrs := []attribute.KeyValue{attribute.String("db.system", "postgresql"), attribute.String("db.statement", data.SQL)}
	if t.serverAddress != "" {
		attrs = append(attrs, attribute.String("server.address", t.serverAddress))
	}
	span.SetAttributes(attrs...)
	return context.WithValue(ctx, dbSpanKey{}, span)
}

// TraceQueryEnd ends the span, recording any error and the command tag.
func (t *DBTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(dbSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(otelcodes.Error, data.Err.Error())
	}
	span.SetAttributes(attribute.String("db.command_tag", data.CommandTag.String()))
	span.End()
}

// WithServerAddress returns a copy of the tracer that records the database server
// address on query spans. It does not mutate the receiver, so one tracer can be
// safely specialized per pool (e.g. read vs write) without cross-contamination.
func (t *DBTracer) WithServerAddress(addr string) *DBTracer {
	if t == nil {
		return nil
	}
	return &DBTracer{tracer: t.tracer, serverAddress: addr}
}

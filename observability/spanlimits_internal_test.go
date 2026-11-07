package observability

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestSpanLimitsBoundAttributeValues pins that the SDK truncates string
// attributes: several copy client-controlled strings (the MCP request id, the
// masked tool arguments), and a remote parent with the sampled flag is always
// sampled, so an unbounded value let an unauthenticated caller size what the
// batch processor retains until export.
func TestSpanLimitsBoundAttributeValues(t *testing.T) {
	limits := spanLimits()
	if limits.AttributeValueLengthLimit != maxSpanAttributeValueBytes {
		t.Fatalf("AttributeValueLengthLimit = %d, want %d", limits.AttributeValueLengthLimit, maxSpanAttributeValueBytes)
	}
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithRawSpanLimits(limits))
	_, span := tp.Tracer("t").Start(context.Background(), "op", trace.WithAttributes(attribute.String("k", strings.Repeat("x", 1<<20))))
	span.End()
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	for _, a := range spans[0].Attributes() {
		if n := len(a.Value.AsString()); n > maxSpanAttributeValueBytes {
			t.Fatalf("attribute %s retained %d bytes, want <= %d", a.Key, n, maxSpanAttributeValueBytes)
		}
	}
}

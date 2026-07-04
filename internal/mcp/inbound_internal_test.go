package mcp

import (
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TestInboundContextStripsBaggageKeepsTrace pins the /mcp trust boundary:
// W3C trace context from the client is continued, but client-supplied baggage
// (attacker-controlled on a public surface) must not enter the dispatch ctx.
func TestInboundContextStripsBaggageKeepsTrace(t *testing.T) {
	old := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(old) })

	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	r.Header.Set("baggage", "tenant=evil,user=admin")

	ctx := inboundContext(r)

	if got := baggage.FromContext(ctx).Len(); got != 0 {
		t.Fatalf("inbound baggage propagated: %d members, want 0", got)
	}
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() {
		t.Fatal("trace context was lost while stripping baggage")
	}
}

package interceptor

import (
	"context"
	"testing"

	"buf.build/go/protovalidate"
	"go.opentelemetry.io/otel/baggage"

	apperr "github.com/yshengliao/gortexa/apperr"
	authpkg "github.com/yshengliao/gortexa/auth"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
)

func TestWithRequestIDBaggagePreservesUpstream(t *testing.T) {
	up, err := baggage.NewMember("tenant", "acme")
	if err != nil {
		t.Fatal(err)
	}
	upBag, err := baggage.New(up)
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), upBag)

	ctx = withRequestIDBaggage(ctx, "req-123")

	b := baggage.FromContext(ctx)
	if got := b.Member("tenant").Value(); got != "acme" {
		t.Errorf("upstream baggage dropped: tenant=%q", got)
	}
	if got := b.Member(RequestIDMetadataKey).Value(); got != "req-123" {
		t.Errorf("request-id baggage missing: %q", got)
	}
}

func TestAuthReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"expired", apperr.Wrap(apperr.CatUnauthenticated, "invalid or expired token", authpkg.ErrExpiredToken), "expired"},
		{"missing", apperr.New(apperr.CatUnauthenticated, "missing authorization"), "missing"},
		{"malformed", apperr.New(apperr.CatUnauthenticated, "malformed authorization"), "invalid"},
		{"generic invalid", apperr.New(apperr.CatUnauthenticated, "invalid or expired token"), "invalid"},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := authReason(c.err); got != c.want {
			t.Errorf("%s: authReason = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestValidationDetailsExtractsFieldAndMessage(t *testing.T) {
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	verr := v.Validate(&resourcev1.GetResourceRequest{Id: ""})
	if verr == nil {
		t.Fatal("expected a validation error for empty Id")
	}
	field, message, ok := validationDetails(verr)
	if !ok {
		t.Fatal("a *protovalidate.ValidationError must be recognized as a client fault")
	}
	if field == "unknown" {
		t.Errorf("field not extracted from violation (still %q)", field)
	}
	if message == "validation failed" {
		t.Errorf("client message not derived from violation (still generic): %q", message)
	}
}

package interceptor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/observability"
)

// TestValidationDetailsPlainError asserts a non-*ValidationError is reported as
// NOT a client fault (ok=false) with the generic fallbacks, so failValidation
// can route it to Internal instead of blaming the caller.
func TestValidationDetailsPlainError(t *testing.T) {
	field, msg, ok := validationDetails(errors.New("rule compilation blew up"))
	if ok {
		t.Fatal("plain error must not be treated as a client validation fault")
	}
	if field != "unknown" || msg != "validation failed" {
		t.Fatalf("fallbacks = %q/%q, want unknown/validation failed", field, msg)
	}
}

// TestFailValidationMapsNonValidationErrorToInternal covers the Internal path: a
// server-side rule fault (compilation/runtime, here a plain error) becomes
// Internal with the cause never serialized to the client.
func TestFailValidationMapsNonValidationErrorToInternal(t *testing.T) {
	cause := errors.New("compiled rule runtime fault")
	err := failValidation(context.Background(), nil, "/svc/M", cause)
	if !apperr.Is(err, apperr.CatInternal) {
		t.Fatalf("non-ValidationError → %v, want Internal", err)
	}
	if st := apperr.ToGRPCStatus(err); strings.Contains(st.Message(), "compiled rule runtime fault") {
		t.Fatalf("internal cause leaked to client: %q", st.Message())
	}
}

// TestFailValidationClientFaultRecordsMetric covers the InvalidArgument branch
// with metrics wired: a real *ValidationError maps to InvalidArgument and
// increments ValidationFails.
func TestFailValidationClientFaultRecordsMetric(t *testing.T) {
	gm, err := observability.NewGovernanceMetrics()
	if err != nil {
		t.Fatal(err)
	}
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	verr := v.Validate(&resourcev1.GetResourceRequest{Id: ""})
	if verr == nil {
		t.Fatal("expected a validation error for empty Id")
	}
	out := failValidation(context.Background(), gm, "/resource.v1.ResourceService/GetResource", verr)
	if !apperr.Is(out, apperr.CatInvalidArgument) {
		t.Fatalf("client fault → %v, want InvalidArgument", out)
	}
}

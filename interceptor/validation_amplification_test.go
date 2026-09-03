package interceptor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	apperr "github.com/yshengliao/gortexa/apperr"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
)

// oneViolation returns a genuine protovalidate violation to clone, so the test
// pins the real rendering path rather than a hand-built stand-in.
func oneViolation(t *testing.T) *protovalidate.Violation {
	t.Helper()
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	err = v.Validate(&resourcev1.GetResourceRequest{Id: ""})
	if err == nil {
		t.Fatal("expected a validation error for empty Id")
	}
	verr, ok := errors.AsType[*protovalidate.ValidationError](err)
	if !ok || len(verr.Violations) == 0 {
		t.Fatalf("expected a *ValidationError with violations, got %T", err)
	}
	return verr.Violations[0]
}

// TestValidationMessageIsBoundedByViolationCount pins the amplification bound:
// protovalidate emits one violation per offending repeated element, so without a
// cap a caller sizes the InvalidArgument message (forwarded verbatim to every
// transport and into the ERROR log record) by sending more bad elements.
func TestValidationMessageIsBoundedByViolationCount(t *testing.T) {
	base := oneViolation(t)

	for _, n := range []int{1, 2, 11, 50_000} {
		verr := &protovalidate.ValidationError{Violations: make([]*protovalidate.Violation, n)}
		for i := range verr.Violations {
			verr.Violations[i] = base
		}

		out := failValidation(context.Background(), nil, "/resource.v1.ResourceService/GetResource", verr)
		if !apperr.Is(out, apperr.CatInvalidArgument) {
			t.Fatalf("n=%d: %v, want InvalidArgument", n, out)
		}
		msg := apperr.ToGRPCStatus(out).Message()
		if len(msg) > maxViolationBytes+64 {
			t.Fatalf("n=%d: gRPC status message is %d bytes, want <= %d", n, len(msg), maxViolationBytes+64)
		}
		if _, body := apperr.ToHTTP(out); len(body.Message) != len(msg) {
			t.Fatalf("n=%d: HTTP body message %d bytes != gRPC %d bytes", n, len(body.Message), len(msg))
		}
		if !strings.Contains(msg, "validation error") {
			t.Fatalf("n=%d: message lost its shape: %q", n, msg)
		}
		if n > maxViolationsInMessage && !strings.Contains(msg, "more)") {
			t.Fatalf("n=%d: truncated message must report the remainder: %q", n, msg)
		}
		if !strings.Contains(msg, "id") {
			t.Fatalf("n=%d: message must still name the offending field: %q", n, msg)
		}
	}
}

// TestValidationDetailsFieldSurvivesBounding guards the other half of
// validationDetails: bounding the message must not disturb field extraction,
// which drives the ValidationFails metric attribute.
func TestValidationDetailsFieldSurvivesBounding(t *testing.T) {
	base := oneViolation(t)
	verr := &protovalidate.ValidationError{Violations: []*protovalidate.Violation{base, base, base}}

	field, msg, ok := validationDetails(verr)
	if !ok {
		t.Fatal("a *ValidationError must be reported as a client fault")
	}
	if field != string(base.FieldDescriptor.Name()) {
		t.Fatalf("field = %q, want %q", field, base.FieldDescriptor.Name())
	}
	if strings.Contains(msg, "\n") {
		t.Fatalf("message must stay single-line: %q", msg)
	}
}

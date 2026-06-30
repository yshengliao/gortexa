package interceptor

import (
	"testing"

	"buf.build/go/protovalidate"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	authpkg "github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

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
	field, message := validationDetails(verr)
	if field == "unknown" {
		t.Errorf("field not extracted from violation (still %q)", field)
	}
	if message == "validation failed" {
		t.Errorf("client message not derived from violation (still generic): %q", message)
	}
}

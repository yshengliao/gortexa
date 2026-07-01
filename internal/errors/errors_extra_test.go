package errors_test

import (
	stderrors "errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestGRPCStatusPassthrough(t *testing.T) {
	// A pre-built gRPC status (e.g. from a downstream call) maps by code.
	src := status.Error(codes.NotFound, "downstream missing")
	if got := apperr.ToGRPCStatus(src).Code(); got != codes.NotFound {
		t.Errorf("passthrough code = %v, want NotFound", got)
	}
	httpCode, body := apperr.ToHTTP(src)
	if httpCode != 404 || body.Code != "not_found" {
		t.Errorf("passthrough http = %d %q", httpCode, body.Code)
	}
	if apperr.ToMCP(src).ErrorCategory != "not_found" {
		t.Errorf("passthrough mcp category wrong")
	}
}

func TestErrorStringAndFields(t *testing.T) {
	base := stderrors.New("root")
	e := apperr.Wrap(apperr.CatUnavailable, "wrapped", base).With("attempt", 3)
	if e.Error() == "" || e.Unwrap() != base {
		t.Fatal("Error/Unwrap broken")
	}
	if e.Fields()["attempt"] != 3 {
		t.Fatalf("fields = %v", e.Fields())
	}
	noCause := apperr.New(apperr.CatNotFound, "x")
	if noCause.Error() != "not_found: x" {
		t.Fatalf("Error() = %q", noCause.Error())
	}
}

func TestLookupAndDefaults(t *testing.T) {
	if len(apperr.DefaultMappings()) < 10 {
		t.Fatal("default mappings too few")
	}
	if _, ok := apperr.Lookup(apperr.CatInternal); !ok {
		t.Fatal("internal mapping should exist")
	}
	if _, ok := apperr.Lookup(apperr.Category("nonexistent")); ok {
		t.Fatal("unknown category should not resolve")
	}
}

func TestToGRPCStatusNilIsOK(t *testing.T) {
	if apperr.ToGRPCStatus(nil).Code() != codes.OK {
		t.Fatal("nil error must map to OK")
	}
}

func TestTypedNilErrorDoesNotPanic(t *testing.T) {
	var typedNil *apperr.Error
	var err error = typedNil

	if got := apperr.ToGRPCStatus(err); got.Code() != codes.Internal || got.Message() != "internal error" {
		t.Fatalf("typed nil status = %v %q, want Internal internal error", got.Code(), got.Message())
	}
	if apperr.Is(err, apperr.CatInternal) {
		t.Fatal("typed nil must not match a category")
	}
}

func TestNonClientErrorCategoriesDoNotLeakCustomMessage(t *testing.T) {
	secret := "acl backend said user=alice password=hunter2"
	cases := []error{
		apperr.New(apperr.CatPermissionDenied, secret),
		status.Error(codes.PermissionDenied, secret),
	}
	for _, err := range cases {
		st := apperr.ToGRPCStatus(err)
		if st.Code() != codes.PermissionDenied || st.Message() != "permission denied" {
			t.Fatalf("gRPC = %v %q, want PermissionDenied permission denied", st.Code(), st.Message())
		}
		_, body := apperr.ToHTTP(err)
		if body.Message != "permission denied" {
			t.Fatalf("HTTP body message = %q, want permission denied", body.Message)
		}
		if got := apperr.ToMCP(err).Message; got != "permission denied" {
			t.Fatalf("MCP message = %q, want permission denied", got)
		}
	}
}

func TestClientErrorCategoriesKeepClientMessage(t *testing.T) {
	if got := apperr.ToGRPCStatus(apperr.New(apperr.CatInvalidArgument, "name is required")).Message(); got != "name is required" {
		t.Fatalf("invalid argument message = %q", got)
	}
	if got := apperr.ToGRPCStatus(apperr.New(apperr.CatUnauthenticated, "missing authorization")).Message(); got != "missing authorization" {
		t.Fatalf("unauthenticated message = %q", got)
	}
}

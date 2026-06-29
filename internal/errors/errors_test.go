package errors_test

import (
	stderrors "errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestMappingMatrix(t *testing.T) {
	cases := []struct {
		cat       apperr.Category
		grpc      codes.Code
		http      int
		retryable bool
	}{
		{apperr.CatInvalidArgument, codes.InvalidArgument, http.StatusBadRequest, false},
		{apperr.CatUnauthenticated, codes.Unauthenticated, http.StatusUnauthorized, false},
		{apperr.CatPermissionDenied, codes.PermissionDenied, http.StatusForbidden, false},
		{apperr.CatNotFound, codes.NotFound, http.StatusNotFound, false},
		{apperr.CatAlreadyExists, codes.AlreadyExists, http.StatusConflict, false},
		{apperr.CatResourceExhausted, codes.ResourceExhausted, http.StatusTooManyRequests, true},
		{apperr.CatDeadlineExceeded, codes.DeadlineExceeded, http.StatusGatewayTimeout, true},
		{apperr.CatUnavailable, codes.Unavailable, http.StatusServiceUnavailable, true},
		{apperr.CatUnimplemented, codes.Unimplemented, http.StatusNotImplemented, false},
		{apperr.CatInternal, codes.Internal, http.StatusInternalServerError, false},
	}
	for _, c := range cases {
		t.Run(string(c.cat), func(t *testing.T) {
			err := apperr.New(c.cat, "boom detail")

			if got := apperr.ToGRPCStatus(err).Code(); got != c.grpc {
				t.Errorf("gRPC code = %v, want %v", got, c.grpc)
			}
			gotHTTP, body := apperr.ToHTTP(err)
			if gotHTTP != c.http {
				t.Errorf("HTTP status = %d, want %d", gotHTTP, c.http)
			}
			if body.Code != string(c.cat) {
				t.Errorf("body.Code = %q, want %q", body.Code, c.cat)
			}
			mcp := apperr.ToMCP(err)
			if !mcp.IsError || mcp.ErrorCategory != string(c.cat) {
				t.Errorf("MCP envelope = %+v", mcp)
			}
			if mcp.IsRetryable != c.retryable {
				t.Errorf("retryable = %v, want %v", mcp.IsRetryable, c.retryable)
			}
		})
	}
}

// The load-bearing invariant: an Internal error never leaks its cause across
// any transport.
func TestInternalNeverLeaks(t *testing.T) {
	secret := "connection to 10.0.0.5 failed: password=hunter2"
	errs := []error{
		stderrors.New(secret),                              // raw, non-*Error
		apperr.Wrap(apperr.CatInternal, secret, stderrors.New(secret)), // explicit internal
	}
	for _, err := range errs {
		st := apperr.ToGRPCStatus(err)
		if st.Code() != codes.Internal {
			t.Errorf("code = %v, want Internal", st.Code())
		}
		if strings.Contains(st.Message(), "hunter2") || strings.Contains(st.Message(), "10.0.0.5") {
			t.Errorf("gRPC message leaked cause: %q", st.Message())
		}
		httpCode, body := apperr.ToHTTP(err)
		if httpCode != http.StatusInternalServerError {
			t.Errorf("http = %d, want 500", httpCode)
		}
		if strings.Contains(body.Message, "hunter2") {
			t.Errorf("HTTP body leaked cause: %q", body.Message)
		}
		mcp := apperr.ToMCP(err)
		if strings.Contains(mcp.Message, "hunter2") {
			t.Errorf("MCP message leaked cause: %q", mcp.Message)
		}
	}
}

func TestErrorAsAndIs(t *testing.T) {
	base := stderrors.New("io failure")
	err := apperr.Wrap(apperr.CatUnavailable, "db down", base)

	if !apperr.Is(err, apperr.CatUnavailable) {
		t.Fatal("Is should match category")
	}
	if !stderrors.Is(err, base) {
		t.Fatal("errors.Is should reach wrapped cause")
	}
	var e *apperr.Error
	if !stderrors.As(err, &e) || e.Category != apperr.CatUnavailable {
		t.Fatal("errors.As should extract *Error")
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate category")
		}
	}()
	r := apperr.NewRegistry(apperr.DefaultMappings()...)
	r.Register(apperr.Mapping{Category: apperr.CatInternal, GRPCCode: codes.Internal, HTTPStatus: 500})
}

func TestGRPCStatusInterface(t *testing.T) {
	// *Error implements interface{ GRPCStatus() *status.Status }, so status.FromError works.
	err := apperr.New(apperr.CatNotFound, "missing")
	if apperr.ToGRPCStatus(err).Code() != codes.NotFound {
		t.Fatal("GRPCStatus interface not honored")
	}
}

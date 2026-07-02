package errors_test

import (
	stderrors "errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestPackageRegisterOnDefault(t *testing.T) {
	// Register is additive and process-local; a brand-new category is safe.
	const cat = apperr.Category("test_registered_cat")
	apperr.Register(apperr.Mapping{
		Category:    cat,
		GRPCCode:    codes.Unknown, // not claimed by any default mapping
		HTTPStatus:  418,
		Retryable:   false,
		SafeMessage: "test registered",
	})
	m, ok := apperr.Lookup(cat)
	if !ok {
		t.Fatal("registered category not found via Lookup")
	}
	if m.HTTPStatus != 418 || m.SafeMessage != "test registered" {
		t.Fatalf("mapping = %+v", m)
	}
	httpCode, body := apperr.ToHTTP(apperr.New(cat, "boom"))
	if httpCode != 418 || body.Code != string(cat) {
		t.Fatalf("ToHTTP = %d %q, want 418 %q", httpCode, body.Code, cat)
	}
}

func TestGRPCStatusMethodNonNil(t *testing.T) {
	// Calling the method directly (not via ToGRPCStatus) exercises the
	// non-nil receiver branch.
	st := apperr.New(apperr.CatNotFound, "gone").GRPCStatus()
	if st.Code() != codes.NotFound {
		t.Fatalf("GRPCStatus() code = %v, want NotFound", st.Code())
	}
}

func TestResolveNilErrorViaHTTPAndMCP(t *testing.T) {
	// ToHTTP/ToMCP funnel nil through resolve (unlike ToGRPCStatus which
	// short-circuits); nil maps to the internal row with an empty message.
	httpCode, body := apperr.ToHTTP(nil)
	if httpCode != 500 || body.Code != string(apperr.CatInternal) {
		t.Fatalf("ToHTTP(nil) = %d %q", httpCode, body.Code)
	}
	if body.Message != "" {
		t.Fatalf("ToHTTP(nil) message = %q, want empty", body.Message)
	}
	if mcp := apperr.ToMCP(nil); mcp.ErrorCategory != string(apperr.CatInternal) || mcp.Message != "" {
		t.Fatalf("ToMCP(nil) = %+v", mcp)
	}
}

func TestClientCategoryEmptyMsgFallsBackToSafeMessage(t *testing.T) {
	cases := []struct {
		name string
		cat  apperr.Category
		want string
	}{
		{"invalid argument", apperr.CatInvalidArgument, "invalid argument"},
		{"unauthenticated", apperr.CatUnauthenticated, "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apperr.ToGRPCStatus(apperr.New(tc.cat, "")).Message(); got != tc.want {
				t.Errorf("message = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusPassthroughClientCodesKeepMessage(t *testing.T) {
	// InvalidArgument/Unauthenticated statuses from downstream keep their
	// message (it is client-facing by contract).
	cases := []struct {
		name string
		code codes.Code
		msg  string
	}{
		{"invalid argument", codes.InvalidArgument, "name is required"},
		{"unauthenticated", codes.Unauthenticated, "missing bearer token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := apperr.ToGRPCStatus(status.Error(tc.code, tc.msg))
			if st.Code() != tc.code || st.Message() != tc.msg {
				t.Errorf("passthrough = %v %q, want %v %q", st.Code(), st.Message(), tc.code, tc.msg)
			}
		})
	}
}

func TestStatusPassthroughInternalCodeNeverLeaks(t *testing.T) {
	// A pre-built Internal status maps back to the internal row and must not
	// leak the downstream message.
	src := status.Error(codes.Internal, "db password rejected")
	st := apperr.ToGRPCStatus(src)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Fatalf("gRPC = %v %q, want Internal internal error", st.Code(), st.Message())
	}
	_, body := apperr.ToHTTP(src)
	if body.Message != "internal error" {
		t.Fatalf("HTTP message = %q, want internal error", body.Message)
	}
}

func TestGRPCStatusNilReceiver(t *testing.T) {
	var e *apperr.Error
	st := e.GRPCStatus()
	if st.Code() != codes.Internal {
		t.Fatalf("nil receiver code = %v, want Internal", st.Code())
	}
	if st.Message() != "internal error" {
		t.Fatalf("nil receiver message = %q, want internal safe message", st.Message())
	}
}

func TestEmptyRegistryInternalFallback(t *testing.T) {
	// A registry with no mappings at all (not even CatInternal) must still
	// resolve everything to the hard-coded internal fallback.
	r := apperr.NewRegistry()
	cases := []struct {
		name string
		err  error
	}{
		{"plain error", stderrors.New("boom")},
		{"typed app error", apperr.New(apperr.CatNotFound, "gone")},
		{"internal app error", apperr.New(apperr.CatInternal, "secret detail")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := r.ToGRPCStatus(tc.err)
			if st.Code() != codes.Internal || st.Message() != "internal error" {
				t.Fatalf("gRPC = %v %q, want Internal internal error", st.Code(), st.Message())
			}
			httpCode, body := r.ToHTTP(tc.err)
			if httpCode != 500 || body.Message != "internal error" {
				t.Fatalf("HTTP = %d %q, want 500 internal error", httpCode, body.Message)
			}
			if mcp := r.ToMCP(tc.err); !mcp.IsError || mcp.Message != "internal error" {
				t.Fatalf("MCP = %+v", mcp)
			}
		})
	}
}

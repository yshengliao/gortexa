package apperr_test

import (
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// TestStatusPassthroughWrappedErrorDoesNotLeakChain pins the passthrough
// branch against status.FromError's wrapped-error path, which rewrites the
// status message to the whole %w chain's Error() text. Only the innermost
// status message is client-facing; a handler's wrapper context (host, DSN,
// SQL) and grpc's own "rpc error: code = ..." boilerplate never are.
func TestStatusPassthroughWrappedErrorDoesNotLeakChain(t *testing.T) {
	cases := []struct {
		name string
		code codes.Code
	}{
		{"invalid argument", codes.InvalidArgument},
		{"unauthenticated", codes.Unauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := status.Error(tc.code, `column "emial" does not exist`)
			wrapped := fmt.Errorf("billing.Lookup at 10.0.7.42:9443 (dsn postgres://svc:hunter2@db.internal:5432/prod): %w", inner)

			st := apperr.ToGRPCStatus(wrapped)

			if st.Code() != tc.code {
				t.Fatalf("code = %v, want %v", st.Code(), tc.code)
			}
			want := `column "emial" does not exist`
			if st.Message() != want {
				t.Fatalf("leaked message: got %q, want only inner message %q", st.Message(), want)
			}
			if _, body := apperr.ToHTTP(wrapped); body.Message != want {
				t.Fatalf("HTTP leaked message: got %q, want %q", body.Message, want)
			}
			if mcp := apperr.ToMCP(wrapped); mcp.Message != want {
				t.Fatalf("MCP leaked message: got %q, want %q", mcp.Message, want)
			}
		})
	}
}

package apperr_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// TestForwardedMessageIsBounded pins the length cap on the two categories whose
// caller-provided message is forwarded verbatim. A handler that builds
// apperr.New(CatInvalidArgument, "bad id: "+req.Id) reflects an arbitrarily
// large request field back into the gRPC status (a multi-MB grpc-message
// trailer most clients then reject as a transport error), the HTTP JSON body
// and the MCP envelope. The head of the message is what tells the caller what
// to fix.
func TestForwardedMessageIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	for _, cat := range []apperr.Category{apperr.CatInvalidArgument, apperr.CatUnauthenticated} {
		err := apperr.New(cat, "bad input: "+huge)
		gm := apperr.ToGRPCStatus(err).Message()
		_, body := apperr.ToHTTP(err)
		mcp := apperr.Default.ToMCP(err)
		for name, msg := range map[string]string{"grpc": gm, "http": body.Message, "mcp": mcp.Message} {
			if len(msg) > 4096+64 {
				t.Errorf("%s/%s: forwarded %d bytes, want <= 4 KiB", cat, name, len(msg))
			}
			if !strings.HasPrefix(msg, "bad input: xxxx") {
				t.Errorf("%s/%s: truncation must keep the head: %q", cat, name, msg[:min(len(msg), 40)])
			}
		}
	}
	// A downstream status with a huge message is bounded the same way.
	st := status.Error(codes.InvalidArgument, "downstream: "+huge)
	if msg := apperr.ToGRPCStatus(st).Message(); len(msg) > 4096+64 {
		t.Errorf("downstream status forwarded %d bytes", len(msg))
	}
	// Short messages are untouched.
	if msg := apperr.ToGRPCStatus(apperr.New(apperr.CatInvalidArgument, "id required")).Message(); msg != "id required" {
		t.Errorf("short message altered: %q", msg)
	}
}

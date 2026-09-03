package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// FuzzHandlePost drives the real MCP bridge HTTP handler (handlePost,
// handleBatchPost, validateRPCRequest, validRPCID, validRPCParams and
// writeRPCValue all at once) with a real loopback gRPC backend, exactly as
// newBridgeServer builds it in bridge_test.go.
func FuzzHandlePost(f *testing.F) {
	seeds := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"resource.v1.ResourceService.GetResource","arguments":{}}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		"[]",
		"[{}]",
		"{",
		"null",
		"[",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	// A 101-element batch.
	var big strings.Builder
	big.WriteByte('[')
	for i := 0; i < 101; i++ {
		if i > 0 {
			big.WriteByte(',')
		}
		big.WriteString(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	}
	big.WriteByte(']')
	f.Add([]byte(big.String()))

	f.Fuzz(func(t *testing.T, data []byte) {
		ts := newBridgeServer(t)

		// Route through the real server so the full transport (bufconn dial,
		// interceptor chain, etc.) is exercised rather than just the handler func.
		resp, err := ts.Client().Post(ts.URL, "application/json", bytes.NewReader(data))
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		switch resp.StatusCode {
		case 200, 202, 400, 403, 405, 413, 415:
		default:
			t.Fatalf("unexpected status %d for input %q", resp.StatusCode, data)
		}

		if len(raw) > 0 {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("body is not valid JSON: %v; body=%q", err, raw)
			}
		}

		if bytes.Contains(raw, []byte("goroutine ")) || bytes.Contains(raw, []byte("panic:")) {
			t.Fatalf("body leaked a panic/goroutine trace: %q", raw)
		}
	})
}

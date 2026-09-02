package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestBridgeBatchPostElementCapEnforced verifies that an unauthenticated
// JSON-RPC batch POST cannot use a small request body to force the bridge to
// produce and buffer an unbounded number of responses. Without a cap on the
// number of batch elements, a ~900KB body of ~300000 trivial invalid entries
// (each rejected at validateRPCRequest, so no gRPC Invoke and none of the
// gRPC governance stages - load shedding, rate limiting, auth - ever run)
// forces the bridge to allocate and buffer one rpcResponse per entry before
// writing a many-megabyte response, all within the 1MiB body cap.
func TestBridgeBatchPostElementCapEnforced(t *testing.T) {
	ts := newBridgeServer(t)

	const n = 300000
	var b strings.Builder
	b.Grow(3*n + 2)
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("{}")
	}
	b.WriteByte(']')
	body := b.String()

	if len(body) >= 1<<20 {
		t.Fatalf("test body %d bytes, must stay under the 1MiB request cap", len(body))
	}

	resp, raw := postRaw(t, ts.URL, "application/json", "", body)

	if resp.StatusCode == http.StatusOK {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatalf("decode batch response: %v", err)
		}
		// A ~900KB request should never be allowed to balloon into hundreds of
		// thousands of buffered responses (tens of MB on the wire). A sane
		// element cap should reject the batch outright (non-200) or truncate
		// it well below this threshold.
		const sanityCap = 1000
		if len(arr) > sanityCap {
			t.Fatalf("batch of %d trivial entries (%d byte body) produced %d responses (%d response bytes) with HTTP 200: "+
				"no batch element cap is enforced, allowing small unauthenticated requests to force large "+
				"allocation/response amplification before any gRPC governance stage (load shedding, rate limit, auth) runs",
				n, len(body), len(arr), len(raw))
		}
	}
	// Any non-200 status (e.g. 413 or a JSON-RPC error status) is treated as
	// the batch having been rejected/capped, which is acceptable.
}

// TestBridgeBatchAtCapStillDispatches pins the boundary: a batch at the cap is
// still served normally, so the guard bounds abuse without breaking clients.
func TestBridgeBatchAtCapStillDispatches(t *testing.T) {
	ts := newBridgeServer(t)

	batch := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		batch = append(batch, map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "ping"})
	}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := postRaw(t, ts.URL, "application/json", "", string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch of 100 = %d, want 200 (body=%s)", resp.StatusCode, raw)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if len(arr) != 100 {
		t.Fatalf("batch of 100 produced %d responses, want 100", len(arr))
	}
}

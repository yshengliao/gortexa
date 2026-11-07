package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestBridgeBatchChainExemptMethodsAreCapped pins the per-batch cap on the
// methods served without Invoke. tools/list, initialize and ping never reach
// the interceptor chain, so rate limiting and auth do not govern them, and
// tools/list returns the whole catalogue: a batch of 100 was a ~45x
// unauthenticated response amplifier on the sample service, growing with the
// consumer's API. One per batch is what a client needs; the rest are refused
// with a small -32600 so the reply stays proportional to the request.
func TestBridgeBatchChainExemptMethodsAreCapped(t *testing.T) {
	ts := newBridgeServer(t)
	for _, method := range []string{"tools/list", "initialize"} {
		batch := make([]map[string]any, 0, 100)
		for i := range 100 {
			batch = append(batch, map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": method})
		}
		body, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		resp, raw := postRaw(t, ts.URL, "application/json", "", string(body)) // no auth, no version header
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s batch = %d, want 200 (body=%s)", method, resp.StatusCode, raw)
		}
		var arr []struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(arr) != 100 {
			t.Fatalf("%s: %d responses, want 100 (every element is answered)", method, len(arr))
		}
		served := 0
		for _, el := range arr {
			switch {
			case el.Error == nil && len(el.Result) > 0:
				served++
			case el.Error != nil && el.Error.Code == -32600:
			default:
				t.Fatalf("%s: unexpected element %+v", method, el)
			}
		}
		if served != 1 {
			t.Fatalf("%s: %d elements served, want exactly 1", method, served)
		}
		if ratio := float64(len(raw)) / float64(len(body)); ratio > 5 {
			t.Fatalf("%s: unauthenticated batch amplifies %dB -> %dB (%.0fx), want <= 5x", method, len(body), len(raw), ratio)
		}
	}
}

// TestBridgeToolsListUnchangedBySerializationCache pins that the
// pre-serialized tools/list result is byte-for-byte what a per-request
// marshal produced: the golden tools/list test covers the shape, this covers
// the batch path and a single request agreeing.
func TestBridgeToolsListSingleStillServed(t *testing.T) {
	ts := newBridgeServer(t)
	_, single := postRaw(t, ts.URL, "application/json", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	_, batch := postRaw(t, ts.URL, "application/json", "", `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`)
	var one map[string]json.RawMessage
	var many []map[string]json.RawMessage
	if err := json.Unmarshal(single, &one); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(batch, &many); err != nil {
		t.Fatal(err)
	}
	if len(many) != 1 || string(many[0]["result"]) != string(one["result"]) {
		t.Fatalf("batch tools/list result differs from single: %s vs %s", batch, single)
	}
	if !strings.Contains(string(one["result"]), `"tools"`) {
		t.Fatalf("tools/list result lost its shape: %s", single)
	}
}

// TestBridgeRejectsOversizedStringID pins the string-id bound. Numeric ids
// were capped at 32 characters; a string id had no cap and was echoed in the
// reply and, for tools/call, copied into a span attribute before the chain
// ran — so an unauthenticated caller sized what the server retained per
// request. 256 bytes is far beyond any real client id.
func TestBridgeRejectsOversizedStringID(t *testing.T) {
	ts := newBridgeServer(t)
	big := strings.Repeat("a", 4096)
	resp, raw := postRaw(t, ts.URL, "application/json", "",
		`{"jsonrpc":"2.0","method":"ping","id":"`+big+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with JSON-RPC error", resp.StatusCode)
	}
	if code := rpcErrorCode(t, raw); code != -32600 {
		t.Fatalf("oversized string id → %s, want -32600 invalid request", raw)
	}
	if strings.Contains(string(raw), big[:64]) {
		t.Fatalf("oversized id was echoed: %d bytes", len(raw))
	}
	// An ordinary string id still round-trips.
	_, ok := postRaw(t, ts.URL, "application/json", "", `{"jsonrpc":"2.0","method":"ping","id":"req-42"}`)
	if !strings.Contains(string(ok), `"id":"req-42"`) {
		t.Fatalf("ordinary string id lost: %s", ok)
	}
}

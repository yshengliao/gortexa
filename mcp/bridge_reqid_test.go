package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// postWithHeaders posts a JSON-RPC body with an explicit bearer token and a
// raw X-Request-Id header value (Go's client/server accept a TAB byte
// unchanged, but gRPC's outgoing-metadata validator does not).
func postWithHeaders(t *testing.T, url, bearer, requestID string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	req.Header.Set("X-Request-Id", requestID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func toolCallBody(id int) []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "get_resource", "arguments": map[string]any{"id": "missing"}},
	})
	return body
}

func toolResult(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	result, _ := out["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no JSON-RPC result: %v", out)
	}
	return result
}

// TestBridgeToolsCallInvalidRequestIDHeader is the regression test for
// R1-G4-2: an X-Request-Id value that net/http happily carries (a raw TAB
// byte) but that gRPC's outgoing-metadata validation rejects (only printable
// bytes in [0x20-0x7E] are allowed) must not surface as an opaque "internal"
// tool error. The gateway path guards this with interceptor.ValidRequestID
// before forwarding; toolsCall must do the same, or grpc-go's client-side
// stream validation trips before the RPC ever reaches the server and apperr
// maps the resulting codes.Internal to the opaque CatInternal SafeMessage -
// even though the caller supplied a valid bearer token.
func TestBridgeToolsCallInvalidRequestIDHeader(t *testing.T) {
	ts := newBridgeServer(t)
	tok := "Bearer " + token(t)

	// Baseline: a well-formed request id round-trips and reaches the service.
	baseResp, baseRaw := postWithHeaders(t, ts.URL, tok, "abc-123", toolCallBody(1))
	if baseResp.StatusCode != http.StatusOK {
		t.Fatalf("baseline: HTTP %d, body=%s", baseResp.StatusCode, baseRaw)
	}
	baseResult := toolResult(t, baseRaw)
	if baseResult["errorCategory"] == "internal" {
		t.Fatalf("baseline call unexpectedly got an internal error: %s", baseRaw)
	}
	wantCategory := baseResult["errorCategory"]

	// The same call with header bytes that gRPC metadata rejects must behave
	// exactly like the baseline: the offending values are dropped, the RPC
	// still reaches the chain, and the service's own answer comes back.
	cases := []struct {
		name      string
		bearer    string
		requestID string
	}{
		{"tab in request id", tok, "a\tb"},
		{"non-ascii request id", tok, "caf\xc3\xa9"},
		{"non-ascii authorization", "Bearer \xc3\xa9", "abc-123"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := postWithHeaders(t, ts.URL, tc.bearer, tc.requestID, toolCallBody(i+2))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP %d, body=%s", resp.StatusCode, raw)
			}
			result := toolResult(t, raw)
			if result["errorCategory"] == "internal" {
				t.Fatalf("unvalidated header reached gRPC metadata and produced an opaque internal error: %s", raw)
			}
			if tc.bearer == tok && result["errorCategory"] != wantCategory {
				t.Fatalf("errorCategory = %v, want %v (same as baseline): %s", result["errorCategory"], wantCategory, raw)
			}
		})
	}
}

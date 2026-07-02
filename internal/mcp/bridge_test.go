package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/internal/mcp"
	"github.com/yshengliao/gortexa/testutil"
)

func newBridgeServer(t *testing.T) *httptest.Server {
	t.Helper()
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	})
	svcs, err := mcp.ServiceDescriptors("resource.v1.ResourceService")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := mcp.NewBridge(conn, svcs, apperr.Default)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(bridge.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func rpc(t *testing.T, url, bearer string, payload map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return out
}

func token(t *testing.T) string {
	t.Helper()
	tok, err := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa").Sign("tester", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestBridgeInitializeAndList(t *testing.T) {
	ts := newBridgeServer(t)

	init := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if init["result"] == nil {
		t.Fatalf("initialize result missing: %v", init)
	}

	list := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools/list returned %d tools, want 4", len(tools))
	}
}

func TestBridgeInitializeNegotiatesVersion(t *testing.T) {
	ts := newBridgeServer(t)

	// A supported protocol version is echoed back to the client.
	got := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05"}})
	res, _ := got["result"].(map[string]any)
	if res == nil || res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("supported version not echoed: %v", got)
	}

	// An unsupported version falls back to the server default.
	got = rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize",
		"params": map[string]any{"protocolVersion": "1999-01-01"}})
	res, _ = got["result"].(map[string]any)
	if res == nil || res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("unsupported version should fall back to default: %v", got)
	}
}

func TestBridgeToolsCallEnforcesAuth(t *testing.T) {
	ts := newBridgeServer(t)

	// Without auth, the call flows through the interceptor chain and the auth
	// interceptor rejects it — surfaced as an MCP tool error (isError:true).
	call := rpc(t, ts.URL, "", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "get_resource", "arguments": map[string]any{"id": "abc"}},
	})
	result, _ := call["result"].(map[string]any)
	if result == nil || result["isError"] != true {
		t.Fatalf("unauthenticated call should be a tool error: %v", call)
	}
	if result["errorCategory"] != "unauthenticated" {
		t.Fatalf("errorCategory = %v, want unauthenticated", result["errorCategory"])
	}
}

func TestBridgeToolsCallRoundTrip(t *testing.T) {
	ts := newBridgeServer(t)
	tok := token(t)

	// create
	create := rpc(t, ts.URL, tok, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "create_resource", "arguments": map[string]any{
			"resource": map[string]any{"name": "alpha", "owner": "u-1"},
		}},
	})
	result, _ := create["result"].(map[string]any)
	if result == nil || result["isError"] != false {
		t.Fatalf("create result = %v", create)
	}
	content, _ := result["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil || created.ID == "" {
		t.Fatalf("created text = %q err=%v", text, err)
	}

	// get it back through the loopback
	get := rpc(t, ts.URL, tok, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "get_resource", "arguments": map[string]any{"id": created.ID}},
	})
	gr, _ := get["result"].(map[string]any)
	if gr == nil || gr["isError"] != false {
		t.Fatalf("get result = %v", get)
	}
}

func TestBridgeUnknownMethod(t *testing.T) {
	ts := newBridgeServer(t)
	resp := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 9, "method": "bogus"})
	if resp["error"] == nil {
		t.Fatalf("unknown method should return a JSON-RPC error: %v", resp)
	}
}

// newBridgeServerWithOrigins builds a bridge whose DNS-rebinding allowlist is set.
func newBridgeServerWithOrigins(t *testing.T, origins []string) *httptest.Server {
	t.Helper()
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	})
	svcs, err := mcp.ServiceDescriptors("resource.v1.ResourceService")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := mcp.NewBridge(conn, svcs, apperr.Default)
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetAllowedOrigins(origins)
	ts := httptest.NewServer(bridge.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postWithOrigin(t *testing.T, url, origin string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestBridgeOriginValidation covers the MCP Streamable-HTTP DNS-rebinding guard.
func TestBridgeOriginValidation(t *testing.T) {
	allow := newBridgeServerWithOrigins(t, []string{"https://good.example"})
	if code := postWithOrigin(t, allow.URL, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("disallowed origin = %d, want 403", code)
	}
	if code := postWithOrigin(t, allow.URL, "https://good.example"); code != http.StatusOK {
		t.Fatalf("allowlisted origin = %d, want 200", code)
	}
	// No Origin header (non-browser client) is always allowed.
	if code := postWithOrigin(t, allow.URL, ""); code != http.StatusOK {
		t.Fatalf("no-origin request = %d, want 200", code)
	}
	// "*" allows any origin.
	star := newBridgeServerWithOrigins(t, []string{"*"})
	if code := postWithOrigin(t, star.URL, "https://anything.example"); code != http.StatusOK {
		t.Fatalf("wildcard origin = %d, want 200", code)
	}
}

// TestBridgeRejectsAmplificationID guards the id-validation DoS fix: an id with a
// huge decimal exponent must be rejected cheaply (no big.Rat expansion), not
// echoed or processed.
func TestBridgeRejectsAmplificationID(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := postRaw(t, ts.URL, "application/json", "",
		`{"jsonrpc":"2.0","method":"ping","id":1e1000000}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with JSON-RPC error", resp.StatusCode)
	}
	if code := rpcErrorCode(t, raw); code != -32600 {
		t.Fatalf("amplification id → %s, want -32600 invalid request", raw)
	}
}

func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }

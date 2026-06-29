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
	tok, err := auth.NewVerifier(testutil.DefaultSecret, "gortexa").Sign("tester", nil, time.Hour)
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

func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }

func postRPCWithHeaders(t *testing.T, url string, contentType string, accept string) *http.Response {
	t.Helper()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestBridgePostRequiresJSONContentType(t *testing.T) {
	ts := newBridgeServer(t)

	t.Run("missing content type", func(t *testing.T) {
		resp := postRPCWithHeaders(t, ts.URL, "", "")
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("POST without content type = %d, want 415", resp.StatusCode)
		}
	})

	t.Run("text plain", func(t *testing.T) {
		resp := postRPCWithHeaders(t, ts.URL, "text/plain", "")
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("POST with text/plain = %d, want 415", resp.StatusCode)
		}
	})

	t.Run("json content type", func(t *testing.T) {
		resp := postRPCWithHeaders(t, ts.URL, "application/json; charset=utf-8", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST with application/json = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
	})
}

func TestBridgePostNegotiatesAccept(t *testing.T) {
	ts := newBridgeServer(t)

	t.Run("text event stream", func(t *testing.T) {
		resp := postRPCWithHeaders(t, ts.URL, "application/json", "text/event-stream")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST with Accept text/event-stream = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("content-type = %q, want text/event-stream", got)
		}
	})

	t.Run("text event stream q zero", func(t *testing.T) {
		resp := postRPCWithHeaders(t, ts.URL, "application/json", "text/event-stream;q=0")
		if resp.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("POST with Accept text/event-stream;q=0 = %d, want 406", resp.StatusCode)
		}
	})
}

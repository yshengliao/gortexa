package mcp_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yshengliao/gortexa/mcp"
)

func TestServiceDescriptorsUnknown(t *testing.T) {
	if _, err := mcp.ServiceDescriptors("no.such.Service"); err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestDowngradeNilSchema(t *testing.T) {
	ir := mcp.ToolIR{Name: "empty"}
	if mcp.DowngradeOpenAI(ir).Function.Parameters != nil {
		t.Error("nil input schema → nil OpenAI parameters")
	}
	if mcp.DowngradeGemini(ir).Parameters != nil {
		t.Error("nil input schema → nil Gemini parameters")
	}
	// MCP defaults an absent destructiveHint to true, so a mutating,
	// non-destructive tool must say destructiveHint:false explicitly.
	b, err := json.Marshal(mcp.DowngradeMCP(ir).Annotations)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"readOnlyHint":false,"destructiveHint":false}` {
		t.Errorf("non-read-only/non-destructive annotations = %s", b)
	}
}

func TestBridgePing(t *testing.T) {
	ts := newBridgeServer(t)
	ping := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if ping["result"] == nil {
		t.Fatalf("ping result missing: %v", ping)
	}
}

func TestBridgeMethodNotAllowed(t *testing.T) {
	ts := newBridgeServer(t)
	req, _ := http.NewRequest(http.MethodPut, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", resp.StatusCode)
	}
}

// GET would open a server→client SSE stream that holds a goroutine until the
// client leaves, with no auth and no shutdown signal. Gortexa never pushes
// messages, so GET is refused outright.
func TestBridgeGetNotAllowed(t *testing.T) {
	ts := newBridgeServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET = %d Allow=%q, want 405 Allow=POST", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

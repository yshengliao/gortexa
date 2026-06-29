package mcp_test

import (
	"net/http"
	"testing"

	"github.com/yshengliao/gortexa/internal/mcp"
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
	if mcp.DowngradeMCP(ir).Annotations != nil {
		t.Error("non-read-only/non-destructive tool → nil annotations")
	}
}

func TestBridgePing(t *testing.T) {
	ts := newBridgeServer(t)
	sessionID := initializeSession(t, ts.URL)
	ping, _ := rpcWithSession(t, ts.URL, "", sessionID, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
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

func TestBridgeGetRequiresValidSession(t *testing.T) {
	ts := newBridgeServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET without session = %d, want 401", resp.StatusCode)
	}
}

func TestBridgeGetOpensSSE(t *testing.T) {
	ts := newBridgeServer(t)
	sessionID := initializeSession(t, ts.URL)
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req) // returns once headers are flushed
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
}

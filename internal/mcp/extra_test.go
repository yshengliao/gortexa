package mcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

func TestBridgeGetOpensSSE(t *testing.T) {
	ts := newBridgeServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	resp, err := http.DefaultClient.Do(req) // returns once headers are flushed
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
}

func rpcRaw(t *testing.T, url string, payload string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func TestBridgeBatchMixedPingAndToolsList(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := rpcRaw(t, ts.URL, `[
		{"jsonrpc":"2.0","id":1,"method":"ping"},
		{"jsonrpc":"2.0","id":2,"method":"tools/list"}
	]`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if len(out) != 2 {
		t.Fatalf("batch response count = %d, want 2: %v", len(out), out)
	}
	if string(mustMarshal(t, out[0]["id"])) != "1" || out[0]["result"] == nil {
		t.Fatalf("ping response = %v", out[0])
	}
	result, _ := out[1]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if string(mustMarshal(t, out[1]["id"])) != "2" || len(tools) != 4 {
		t.Fatalf("tools/list response = %v", out[1])
	}
}

func TestBridgeBatchOmitsNotificationResponse(t *testing.T) {
	ts := newBridgeServer(t)
	_, raw := rpcRaw(t, ts.URL, `[
		{"jsonrpc":"2.0","method":"ping"},
		{"jsonrpc":"2.0","id":7,"method":"ping"}
	]`)
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if len(out) != 1 {
		t.Fatalf("batch response count = %d, want 1: %v", len(out), out)
	}
	if string(mustMarshal(t, out[0]["id"])) != "7" {
		t.Fatalf("response id = %v, want 7", out[0]["id"])
	}
}

func TestBridgeBatchAllNotificationsAccepted(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := rpcRaw(t, ts.URL, `[
		{"jsonrpc":"2.0","method":"ping"},
		{"jsonrpc":"2.0","method":"tools/list"}
	]`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("all-notification batch status = %d, want 202; body=%s", resp.StatusCode, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("all-notification batch body = %s, want empty", raw)
	}
}

func TestBridgeEmptyBatchInvalidRequest(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := rpcRaw(t, ts.URL, `[]`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty batch status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["code"] != float64(-32600) {
		t.Fatalf("empty batch error code = %v, want -32600; response=%v", errObj["code"], out)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// A panic inside the POST dispatch path must be recovered and returned as a clean
// JSON-RPC -32603, not a dropped connection. A tool with a nil Input descriptor
// makes toolsCall panic inside dynamicpb/protojson, exercising the recover wrapper.
func TestBridgePanicRecovery(t *testing.T) {
	b := &Bridge{
		reg:   apperr.Default,
		tools: map[string]ToolIR{"boom": {Name: "boom", FullMethod: "/x.Y/Z"}},
		order: []string{"boom"},
	}
	ts := httptest.NewServer(b.Handler())
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{"x":1}}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request failed (a dropped connection would mean no recovery): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a JSON-RPC error", resp.StatusCode)
	}
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32603 {
		t.Fatalf("want JSON-RPC -32603 (internal error), got %+v", out)
	}
}

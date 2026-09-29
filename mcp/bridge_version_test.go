package mcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postVersion POSTs body with the given MCP-Protocol-Version header values
// (none: header absent).
func postVersion(t *testing.T, url, body string, versions ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, v := range versions {
		req.Header.Add("MCP-Protocol-Version", v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestBridgeInitializeNegotiatesEveryRevision(t *testing.T) {
	ts := newBridgeServer(t)
	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
		got := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": v}})
		res, _ := got["result"].(map[string]any)
		if res == nil || res["protocolVersion"] != v {
			t.Fatalf("initialize(%s) = %v, want the version echoed", v, got)
		}
	}
	// No requested version: the newest revision is offered.
	got := rpc(t, ts.URL, "", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize"})
	if res, _ := got["result"].(map[string]any); res == nil || res["protocolVersion"] != "2025-11-25" {
		t.Fatalf("default initialize = %v, want 2025-11-25", got)
	}
}

func TestBridgeProtocolVersionHeader(t *testing.T) {
	ts := newBridgeServer(t)
	const ping = `{"jsonrpc":"2.0","id":7,"method":"ping"}`
	const note = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

	for _, tc := range []struct {
		name     string
		body     string
		versions []string
		want     int
	}{
		{"absent", ping, nil, http.StatusOK},
		{"2025-11-25", ping, []string{"2025-11-25"}, http.StatusOK},
		{"2025-06-18", ping, []string{"2025-06-18"}, http.StatusOK},
		{"2025-03-26", ping, []string{"2025-03-26"}, http.StatusOK},
		{"2024-11-05", ping, []string{"2024-11-05"}, http.StatusOK},
		{"unsupported", ping, []string{"1999-01-01"}, http.StatusBadRequest},
		{"garbage", ping, []string{"not a version"}, http.StatusBadRequest},
		{"empty", ping, []string{""}, http.StatusBadRequest},
		{"repeated", ping, []string{"2025-06-18", "2025-11-25"}, http.StatusBadRequest},
		{"notification unsupported", note, []string{"1999-01-01"}, http.StatusBadRequest},
		{"notification supported", note, []string{"2025-11-25"}, http.StatusAccepted},
		// initialize negotiates in its body; a stale header must not block it.
		{"initialize unsupported", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`, []string{"1999-01-01"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := postVersion(t, ts.URL, tc.body, tc.versions...)
			if status != tc.want {
				t.Fatalf("status = %d (%s), want %d", status, raw, tc.want)
			}
			if status != http.StatusBadRequest {
				return
			}
			var resp struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if resp.Error.Code != -32600 || strings.Contains(string(raw), "1999") {
				t.Fatalf("error = %s, want -32600 without echoing the header", raw)
			}
			if tc.body == ping && string(resp.ID) != "7" {
				t.Fatalf("id = %s, want the request id", resp.ID)
			}
		})
	}
}

func TestBridgeBatchingByVersion(t *testing.T) {
	ts := newBridgeServer(t)
	const batch = `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`

	for _, tc := range []struct {
		name     string
		versions []string
		want     int
	}{
		{"absent assumes 2025-03-26", nil, http.StatusOK},
		{"2025-03-26", []string{"2025-03-26"}, http.StatusOK},
		{"2024-11-05", []string{"2024-11-05"}, http.StatusOK},
		{"2025-06-18", []string{"2025-06-18"}, http.StatusBadRequest},
		{"2025-11-25", []string{"2025-11-25"}, http.StatusBadRequest},
		{"unsupported", []string{"1999-01-01"}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := postVersion(t, ts.URL, batch, tc.versions...)
			if status != tc.want {
				t.Fatalf("status = %d (%s), want %d", status, raw, tc.want)
			}
			if status == http.StatusOK {
				var arr []map[string]any
				if err := json.Unmarshal(raw, &arr); err != nil || len(arr) != 2 {
					t.Fatalf("batch response = %s, want two responses", raw)
				}
				return
			}
			var resp map[string]any
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if e, _ := resp["error"].(map[string]any); e == nil || e["code"] != float64(-32600) {
				t.Fatalf("rejection = %s, want a -32600 JSON-RPC error", raw)
			}
		})
	}
}

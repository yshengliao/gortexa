package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sizedPingRequest(t *testing.T, size int) []byte {
	t.Helper()
	prefix := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`)
	suffix := []byte(`"}}`)
	padLen := size - len(prefix) - len(suffix)
	if padLen < 0 {
		t.Fatalf("request size %d is too small for test payload overhead %d", size, len(prefix)+len(suffix))
	}
	body := append(append(append([]byte{}, prefix...), bytes.Repeat([]byte("a"), padLen)...), suffix...)
	if len(body) != size {
		t.Fatalf("body length = %d, want %d", len(body), size)
	}
	return body
}

func postRawMCP(t *testing.T, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	(&Bridge{}).Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	t.Cleanup(func() { _ = resp.Body.Close() })
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return resp, decoded
}

func TestBridgeAcceptsExactlyMaxRequestBytes(t *testing.T) {
	resp, decoded := postRawMCP(t, sizedPingRequest(t, maxRequestBytes))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if decoded["error"] != nil {
		t.Fatalf("exactly max request bytes returned error: %v", decoded)
	}
	if decoded["result"] == nil {
		t.Fatalf("exactly max request bytes should return ping result: %v", decoded)
	}
}

func TestBridgeRejectsMaxRequestBytesPlusOneAsTooLarge(t *testing.T) {
	resp, decoded := postRawMCP(t, sizedPingRequest(t, maxRequestBytes+1))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
	errResp, _ := decoded["error"].(map[string]any)
	if errResp == nil {
		t.Fatalf("too-large request should return JSON-RPC error: %v", decoded)
	}
	if errResp["message"] != "request entity too large" {
		t.Fatalf("error message = %v, want request entity too large", errResp["message"])
	}
	if errResp["message"] == "parse error" {
		t.Fatalf("too-large request returned parse error: %v", decoded)
	}
}

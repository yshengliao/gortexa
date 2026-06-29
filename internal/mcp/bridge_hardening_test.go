package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postRaw posts a raw body with explicit Content-Type / Accept headers and
// returns the response plus its body.
func postRaw(t *testing.T, url, contentType, accept, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
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
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestBridgeRejectsNonJSONContentType(t *testing.T) {
	ts := newBridgeServer(t)
	for _, ct := range []string{"", "text/plain", "application/octet-stream"} {
		resp, _ := postRaw(t, ts.URL, ct, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("content-type %q = %d, want 415", ct, resp.StatusCode)
		}
	}
}

func TestBridgeRejectsOversizedBody(t *testing.T) {
	ts := newBridgeServer(t)
	big := strings.Repeat("a", (1<<20)+16)
	resp, _ := postRaw(t, ts.URL, "application/json", "", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", resp.StatusCode)
	}
}

func TestBridgeRejectsInvalidRequests(t *testing.T) {
	ts := newBridgeServer(t)
	for _, body := range []string{
		`{}`,
		`{"jsonrpc":"1.0","method":"ping","id":1}`,
		`{"jsonrpc":"2.0","id":1}`,                                   // missing method
		`{"jsonrpc":"2.0","method":"ping","id":1.5}`,                 // fractional id
		`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":[]}`, // tools/call params must be object
		`42`,
	} {
		resp, raw := postRaw(t, ts.URL, "application/json", "", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("body %q status = %d, want 200 with JSON-RPC error", body, resp.StatusCode)
		}
		var out struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if out.Error == nil || out.Error.Code != -32600 {
			t.Fatalf("body %q → %s, want -32600 invalid request", body, raw)
		}
	}
}

func TestBridgeBatchRequests(t *testing.T) {
	ts := newBridgeServer(t)

	// A mixed batch returns one response per non-notification request.
	resp, raw := postRaw(t, ts.URL, "application/json", "",
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d", resp.StatusCode)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("batch decode %s: %v", raw, err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch returned %d responses, want 2: %s", len(arr), raw)
	}

	// A batch whose only entries are notifications is acked with 202, no body.
	resp, raw = postRaw(t, ts.URL, "application/json", "",
		`[{"jsonrpc":"2.0","method":"ping"},{"jsonrpc":"2.0","method":"ping"}]`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("all-notification batch = %d, want 202", resp.StatusCode)
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		t.Fatalf("202 batch should have no body, got %s", raw)
	}

	// An empty batch is an invalid request.
	resp, raw = postRaw(t, ts.URL, "application/json", "", `[]`)
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.Error == nil || out.Error.Code != -32600 {
		t.Fatalf("empty batch → status %d body %s, want -32600", resp.StatusCode, raw)
	}
}

func TestBridgeAcceptNegotiation(t *testing.T) {
	ts := newBridgeServer(t)

	// Explicitly accepting SSE yields an event-stream response.
	resp, raw := postRaw(t, ts.URL, "application/json", "text/event-stream",
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE accept = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(string(raw), "event: message") {
		t.Fatalf("SSE body = %q", raw)
	}

	// An Accept that admits no supported type is 406.
	resp, _ = postRaw(t, ts.URL, "application/json", "application/xml",
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if resp.StatusCode != http.StatusNotAcceptable {
		t.Fatalf("unacceptable Accept = %d, want 406", resp.StatusCode)
	}
}

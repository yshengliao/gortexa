package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/internal/mcp"
	"github.com/yshengliao/gortexa/testutil"
)

// rpcIDAndError decodes the JSON-RPC id (as raw text) and error code from a
// single response, so tests can assert id:null on error paths without depending
// on volatile error strings.
func rpcIDAndError(t *testing.T, raw []byte) (id string, code int, hasError bool) {
	t.Helper()
	var out struct {
		ID    json.RawMessage `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if out.Error != nil {
		return string(out.ID), out.Error.Code, true
	}
	return string(out.ID), 0, false
}

// TestBridgeValidIntegerIDsEchoed covers validRPCID's accept path: integer ids
// pass validation and are echoed verbatim in the response.
func TestBridgeValidIntegerIDsEchoed(t *testing.T) {
	ts := newBridgeServer(t)
	for _, want := range []string{"0", "-5", "123"} {
		resp, raw := postRaw(t, ts.URL, "application/json", "",
			`{"jsonrpc":"2.0","method":"ping","id":`+want+`}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("id %s status = %d, want 200", want, resp.StatusCode)
		}
		id, _, hasErr := rpcIDAndError(t, raw)
		if hasErr {
			t.Fatalf("valid id %s should not error: %s", want, raw)
		}
		if id != want {
			t.Fatalf("id echoed = %s, want %s", id, want)
		}
	}
}

// TestBridgeStringIDEchoed covers validRPCID's string branch: a string id is
// valid and echoed verbatim.
func TestBridgeStringIDEchoed(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := postRaw(t, ts.URL, "application/json", "",
		`{"jsonrpc":"2.0","method":"ping","id":"req-7"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	id, _, hasErr := rpcIDAndError(t, raw)
	if hasErr || id != `"req-7"` {
		t.Fatalf("string id echoed = %s hasErr=%v: %s", id, hasErr, raw)
	}
}

// TestBridgeValidIDInvalidParams covers validateRPCRequest's idOrNull path: a
// valid id with a structurally invalid params value yields -32600 but still
// reports the caller's id (params failure doesn't lose the id).
func TestBridgeValidIDInvalidParams(t *testing.T) {
	ts := newBridgeServer(t)
	// params is a bare number, not the required object/array structured value.
	resp, raw := postRaw(t, ts.URL, "application/json", "",
		`{"jsonrpc":"2.0","method":"ping","id":42,"params":5}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	id, code, hasErr := rpcIDAndError(t, raw)
	if !hasErr || code != -32600 {
		t.Fatalf("invalid params → %s, want -32600", raw)
	}
	if id != "42" {
		t.Fatalf("id = %s, want 42 preserved through params failure: %s", id, raw)
	}
}

// TestBridgeRejectsMalformedIDs covers validRPCID's reject paths: fractional,
// exponent, over-length, and huge-exponent numeric ids all yield -32600 with
// id:null (an unusable id can't be echoed).
func TestBridgeRejectsMalformedIDs(t *testing.T) {
	ts := newBridgeServer(t)
	cases := map[string]string{
		"fractional":    "1.5",
		"exponent":      "1e3",
		"overlength":    strings.Repeat("9", 33), // > maxRPCIDNumberLen (32)
		"huge exponent": "1e1000000",
	}
	for name, idLit := range cases {
		t.Run(name, func(t *testing.T) {
			resp, raw := postRaw(t, ts.URL, "application/json", "",
				`{"jsonrpc":"2.0","method":"ping","id":`+idLit+`}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 with JSON-RPC error", resp.StatusCode)
			}
			id, code, hasErr := rpcIDAndError(t, raw)
			if !hasErr || code != -32600 {
				t.Fatalf("id %s → %s, want -32600 invalid request", idLit, raw)
			}
			if id != "null" {
				t.Fatalf("rejected id must be reported as null, got %q: %s", id, raw)
			}
		})
	}
}

// TestBridgeErrorsCarryNullID checks the parse/oversize/empty-body error paths
// all report id:null (the id can't be recovered from an unparseable request).
func TestBridgeErrorsCarryNullID(t *testing.T) {
	ts := newBridgeServer(t)
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   int
	}{
		{"parse error", `{not json`, http.StatusOK, -32700},
		{"empty body", "   \n\t ", http.StatusOK, -32700},
		{"oversize", strings.Repeat("a", (1<<20)+16), http.StatusRequestEntityTooLarge, -32000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := postRaw(t, ts.URL, "application/json", "", tc.body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			id, code, hasErr := rpcIDAndError(t, raw)
			if !hasErr || code != tc.wantCode {
				t.Fatalf("code = %d hasErr=%v, want %d: %s", code, hasErr, tc.wantCode, raw)
			}
			if id != "null" {
				t.Fatalf("error response id = %q, want null: %s", id, raw)
			}
		})
	}
}

// TestNewBridgeDuplicateToolName registers the same service twice so its tool
// names collide, which NewBridge must reject.
func TestNewBridgeDuplicateToolName(t *testing.T) {
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	})
	svcs, err := mcp.ServiceDescriptors("resource.v1.ResourceService")
	if err != nil {
		t.Fatal(err)
	}
	dup := append(svcs, svcs...) // same ServiceDescriptor twice → duplicate tool names
	if _, err := mcp.NewBridge(conn, dup, apperr.Default); err == nil {
		t.Fatal("duplicate tool name across services must error")
	}
}

// getWithOrigin issues a GET (the SSE-open verb) with an optional Origin header
// and returns the status. A disallowed origin is rejected before any stream is
// opened, so the request completes immediately.
func getWithOrigin(t *testing.T, url, origin string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
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

// TestBridgeOriginValidationGET covers the DNS-rebinding guard on the GET/SSE
// surface plus the empty-allowlist default.
func TestBridgeOriginValidationGET(t *testing.T) {
	allow := newBridgeServerWithOrigins(t, []string{"https://good.example"})
	if code := getWithOrigin(t, allow.URL, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("GET disallowed origin = %d, want 403", code)
	}

	// Empty allowlist + no Origin header (non-browser client) is allowed.
	empty := newBridgeServerWithOrigins(t, nil)
	if code := postWithOrigin(t, empty.URL, ""); code != http.StatusOK {
		t.Fatalf("empty allowlist no-origin = %d, want 200", code)
	}
}

// TestBridgeTextWildcardAccept covers negotiateResponseType's text/* branch:
// a text/* Accept negotiates to an SSE response.
func TestBridgeTextWildcardAccept(t *testing.T) {
	ts := newBridgeServer(t)
	resp, raw := postRaw(t, ts.URL, "application/json", "text/*",
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("text/* accept = %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(string(raw), "event: message") {
		t.Fatalf("SSE body = %q", raw)
	}
}

// TestBridgeSSEHonorsCallerStatus checks writeRPCValue does not downgrade an
// error status to 200 when the client requested SSE: an oversized body returns
// 413 as an event-stream frame.
func TestBridgeSSEHonorsCallerStatus(t *testing.T) {
	ts := newBridgeServer(t)
	big := strings.Repeat("a", (1<<20)+16)
	resp, raw := postRaw(t, ts.URL, "application/json", "text/event-stream", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized SSE = %d, want 413", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(string(raw), "event: message") {
		t.Fatalf("SSE body = %q", raw)
	}
}

// TestBridgeMethodNotAllowedSetsAllow asserts a rejected verb advertises the
// permitted methods via the Allow header.
func TestBridgeMethodNotAllowedSetsAllow(t *testing.T) {
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
	if allow := resp.Header.Get("Allow"); allow != "GET, POST" {
		t.Fatalf("Allow header = %q, want %q", allow, "GET, POST")
	}
}

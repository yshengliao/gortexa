package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/testutil"
)

// amplBatchBody builds an n-element JSON-RPC batch of tools/call requests
// against the resource.v1 get_resource tool.
func amplBatchBody(n int) []byte {
	els := make([]any, 0, n)
	for i := 0; i < n; i++ {
		els = append(els, map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": "tools/call",
			"params": map[string]any{"name": "get_resource", "arguments": map[string]any{"id": "abc"}},
		})
	}
	b, _ := json.Marshal(els)
	return b
}

// TestBridgeOversizedAuthorizationIsNotForwarded pins the length bound itself:
// a *valid* credential that exceeds maxAuthzBytes must be dropped rather than
// replayed downstream, so the call comes back unauthenticated. Without the
// bound the same oversized-but-genuine token authenticates, which is exactly
// what lets an attacker's ~900KB header ride the loopback once per batch
// element.
func TestBridgeOversizedAuthorizationIsNotForwarded(t *testing.T) {
	ts := newBridgeServer(t)
	verifier := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa")

	sign := func(roles []string) string {
		t.Helper()
		tok, err := verifier.Sign("tester", roles, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	// Baseline: an ordinary token reaches the service, so "unauthenticated"
	// below is attributable to the bound and not to the fixture.
	base := toolResult(t, mustPost(t, ts.URL, "Bearer "+sign(nil), toolCallBody(1)))
	if base["errorCategory"] == "unauthenticated" {
		t.Fatalf("baseline token was rejected: %v", base)
	}

	// A genuine token padded past the bound (a role claim does the padding, so
	// the signature still verifies) must never reach the auth stage.
	big := "Bearer " + sign([]string{strings.Repeat("A", 16<<10)})
	if len(big) <= 8<<10 {
		t.Fatalf("padded credential is only %d bytes, not over the bound", len(big))
	}
	over := toolResult(t, mustPost(t, ts.URL, big, toolCallBody(2)))
	if over["errorCategory"] != "unauthenticated" {
		t.Fatalf("oversized Authorization header was forwarded downstream (errorCategory=%v); it must be dropped before it can be replayed onto every batch element", over["errorCategory"])
	}
}

// TestBridgeBatchAuthorizationHeaderAmplification is the end-to-end guard for
// the amplification the bound closes: the bridge forwards the raw inbound
// Authorization header onto every gRPC Invoke in a JSON-RPC batch, so an
// unauthenticated caller can send a ~900KB Authorization header alongside a
// small (~10KB) 100-element batch body and have the server re-encode that
// header onto 100 separate loopback Invokes before the auth stage rejects any
// of them. The attacker pays for the header once on the wire; the server pays
// it maxBatchElements times over.
//
// Control: an honest client sending the identical 100-element batch with a
// realistic small (16-byte) Authorization value. The measured ratio is
// wall-clock(large header)/wall-clock(small header) for the same batch size.
func TestBridgeBatchAuthorizationHeaderAmplification(t *testing.T) {
	ts := newBridgeServer(t)

	const batchN = 100
	body := amplBatchBody(batchN)

	send := func(authzVal string) time.Duration {
		req, err := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authzVal)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return time.Since(start)
	}

	// Warm up connection/handler paths so the first measured sample isn't
	// paying one-time setup cost.
	_ = send("Bearer warmup")

	controlDur := send("Bearer " + strings.Repeat("A", 16))
	attackDur := send("Bearer " + strings.Repeat("A", 921600))

	t.Logf("batch=%d control(authz=16B)=%v attack(authz=921600B)=%v ratio=%.1fx",
		batchN, controlDur, attackDur, float64(attackDur)/float64(controlDur))

	// With the length bound in place the oversized value is dropped before it
	// can be replayed onto every element's loopback Invoke, so the batch costs
	// roughly the same regardless of the header's size. maxAcceptableRatio is a
	// generous ceiling so the test only fails on genuine amplification, not on
	// ordinary noise.
	const maxAcceptableRatio = 5.0
	ratio := float64(attackDur) / float64(controlDur)
	if ratio > maxAcceptableRatio {
		t.Fatalf("large-Authorization-header batch cost %.1fx the small-Authorization-header control (control=%v attack=%v) for the identical %d-element batch; expected at most %.0fx - the bridge is replaying the unbounded inbound Authorization header onto every batch element's loopback Invoke before auth ever rejects it, an attacker-observable CPU amplification", ratio, controlDur, attackDur, batchN, maxAcceptableRatio)
	}
}

// mustPost sends one JSON-RPC body with an explicit Authorization value and
// returns the response payload.
func mustPost(t *testing.T, url, authz string, body []byte) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authz)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d, body=%s", resp.StatusCode, raw)
	}
	return raw
}

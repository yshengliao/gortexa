package mcp_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// A rejected Origin is an attempted DNS rebinding and an over-cap batch is an
// attempted amplification. Both were answered with a status code and nothing
// else, so neither attempt appeared in any operator-visible record.
func TestBridgeLogsDeniedOrigin(t *testing.T) {
	var buf bytes.Buffer
	restore := captureDefaultLogger(t, &buf)
	defer restore()

	ts := newBridgeServerWithOrigins(t, []string{"https://allowed.example"})
	if code := postWithOrigin(t, ts.URL, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}

	out := buf.String()
	if !strings.Contains(out, "disallowed origin") {
		t.Fatalf("a rejected origin must be logged; got:\n%s", out)
	}
	if !strings.Contains(out, "https://evil.example") {
		t.Errorf("the log must name the rejected origin; got:\n%s", out)
	}
}

func TestBridgeLogsOversizedBatch(t *testing.T) {
	var buf bytes.Buffer
	restore := captureDefaultLogger(t, &buf)
	defer restore()

	ts := newBridgeServer(t)
	batch := make([]map[string]any, 0, 101)
	for i := range 101 {
		batch = append(batch, map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "ping"})
	}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}

	out := buf.String()
	if !strings.Contains(out, "oversized JSON-RPC batch") {
		t.Fatalf("an over-cap batch must be logged; got:\n%s", out)
	}
	if !strings.Contains(out, "101") {
		t.Errorf("the log must report the element count; got:\n%s", out)
	}
}

// captureDefaultLogger redirects slog.Default for the duration of a test. The
// bridge takes its logger from slog.Default at construction, matching mq.
func captureDefaultLogger(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	return func() { slog.SetDefault(prev) }
}

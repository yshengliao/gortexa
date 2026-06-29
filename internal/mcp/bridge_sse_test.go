package mcp

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type flushRecorder struct {
	header http.Header
	buf    bytes.Buffer
	code   int

	mu      sync.Mutex
	flushes int
	writes  chan string
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}, writes: make(chan string, 8)}
}

func (r *flushRecorder) Header() http.Header { return r.header }

func (r *flushRecorder) WriteHeader(code int) { r.code = code }

func (r *flushRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.buf.Write(p)
	select {
	case r.writes <- string(p):
	default:
	}
	return n, err
}

func (r *flushRecorder) Flush() {
	r.mu.Lock()
	r.flushes++
	r.mu.Unlock()
}

func (r *flushRecorder) flushCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushes
}

func TestBridgeGetRejectsMissingOrInvalidSession(t *testing.T) {
	bridge := &Bridge{sessions: map[string]struct{}{"valid": {}}}

	for _, tc := range []struct {
		name      string
		sessionID string
	}{
		{name: "missing"},
		{name: "invalid", sessionID: "bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFlushRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/mcp", nil)
			if tc.sessionID != "" {
				req.Header.Set("Mcp-Session-Id", tc.sessionID)
			}
			bridge.handleGet(rec, req)
			if rec.code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.code, http.StatusUnauthorized)
			}
		})
	}
}

func TestBridgeGetRejectsUnsupportedResume(t *testing.T) {
	bridge := &Bridge{sessions: map[string]struct{}{"valid": {}}}
	rec := newFlushRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "valid")
	req.Header.Set("Last-Event-ID", "42")

	bridge.handleGet(rec, req)
	if rec.code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.code, http.StatusConflict)
	}
}

func TestBridgeGetSSEFlushesKeepAliveAndCleansUpOnCancel(t *testing.T) {
	oldInterval := ssePingInterval
	ssePingInterval = time.Millisecond
	defer func() { ssePingInterval = oldInterval }()

	bridge := &Bridge{sessions: map[string]struct{}{"valid": {}}}
	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/mcp", nil)
	req.Header.Set("Mcp-Session-Id", "valid")

	done := make(chan struct{})
	go func() {
		bridge.handleGet(rec, req)
		close(done)
	}()

	select {
	case got := <-rec.writes:
		if got != ssePingFrame {
			t.Fatalf("keep-alive frame = %q, want %q", got, ssePingFrame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for keep-alive")
	}
	if rec.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", rec.header.Get("Content-Type"))
	}
	if rec.flushCount() < 2 {
		t.Fatalf("flush count = %d, want at least initial flush and keep-alive flush", rec.flushCount())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after cancellation")
	}
}

func TestWriteRPCSSEFlushesFrame(t *testing.T) {
	rec := newFlushRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")

	writeRPC(rec, req, rpcResponse{JSONRPC: "2.0", Result: map[string]any{}})
	if rec.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", rec.header.Get("Content-Type"))
	}
	if rec.flushCount() != 1 {
		t.Fatalf("flush count = %d, want 1", rec.flushCount())
	}
	if !bytes.Contains(rec.buf.Bytes(), []byte("event: message\n")) {
		t.Fatalf("SSE frame missing event line: %q", rec.buf.String())
	}
}

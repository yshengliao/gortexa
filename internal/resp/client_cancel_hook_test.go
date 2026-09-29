package resp

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// hookConn lets a test land a ctx cancel at an exact point inside roundtrip.
type hookConn struct {
	net.Conn
	onRead             func()
	onSetWriteDeadline func()
	onSetDeadline      func()
}

func (h *hookConn) Read(b []byte) (int, error) {
	n, err := h.Conn.Read(b)
	if f := h.onRead; f != nil {
		h.onRead = nil
		f()
	}
	return n, err
}

func (h *hookConn) SetWriteDeadline(t time.Time) error {
	if f := h.onSetWriteDeadline; f != nil {
		h.onSetWriteDeadline = nil
		f()
	}
	return h.Conn.SetWriteDeadline(t)
}

func (h *hookConn) SetDeadline(t time.Time) error {
	if h.onSetDeadline != nil {
		h.onSetDeadline()
	}
	return h.Conn.SetDeadline(t)
}

func pooledConn(c *Client, h *hookConn) {
	c.idle = append(c.idle, &conn{nc: h, br: bufio.NewReader(h), bw: bufio.NewWriter(h)})
}

// TestRoundtripLateCancelHookDiscardsConn pins that a connection whose cancel
// hook fired during a successful command is not parked: stop() does not wait
// for a hook already started, so that hook can trip SetDeadline(now) on the
// next request to check the connection out and fail it spuriously.
func TestRoundtripLateCancelHookDiscardsConn(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	go func() {
		if _, err := io.ReadFull(server, make([]byte, len("*1\r\n$4\r\nPING\r\n"))); err != nil {
			return
		}
		_, _ = server.Write([]byte("+PONG\r\n"))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient(Options{Addr: "unused", ReadTimeout: -1, WriteTimeout: -1})
	defer func() { _ = c.Close() }()
	// Cancel as the reply arrives: past the pre-read ctx re-check, before stop.
	pooledConn(c, &hookConn{Conn: client, onRead: cancel})

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	c.mu.Lock()
	idle := len(c.idle)
	c.mu.Unlock()
	if idle != 0 {
		t.Fatalf("idle = %d, want 0: a connection with a fired cancel hook was parked for reuse", idle)
	}
}

// TestRoundtripCancelBeforeWriteDeadline pins that a cancel landing between
// the hook registration and SetWriteDeadline is not lost: arming a disabled
// write deadline clears the one the hook just set, and without a re-check the
// write blocks forever on a peer that has stopped reading.
func TestRoundtripCancelBeforeWriteDeadline(t *testing.T) {
	client, server := net.Pipe() // server never reads: writes block
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{})
	var once sync.Once
	h := &hookConn{Conn: client}
	h.onSetDeadline = func() { once.Do(func() { close(fired) }) }
	h.onSetWriteDeadline = func() {
		cancel()
		<-fired // the hook's SetDeadline(now) has run and is about to be overwritten
	}
	c := NewClient(Options{Addr: "unused", ReadTimeout: -1, WriteTimeout: -1})
	defer func() { _ = c.Close() }()
	pooledConn(c, h)

	done := make(chan error, 1)
	go func() { done <- c.Ping(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ping after cancel returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write blocked after ctx cancel: the cleared write deadline swallowed the cancel")
	}
}

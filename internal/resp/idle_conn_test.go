package resp_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/resp"
)

// TestClientReusesDeadIdleConnection pins that a connection the peer closed
// while it was parked idle in the pool does not surface as a hard failure to
// the caller: the client discards it and retries once on a fresh connection.
// A Redis `timeout N`, a restart/failover, or an LB reaping idle flows makes
// this the normal first command after any quiet period.
func TestClientReusesDeadIdleConnection(t *testing.T) {
	var dials atomic.Int64
	// Serve exactly one GET per connection, then FIN — the same shape as an
	// idle-timeout reap arriving while the connection sits parked.
	addr := serveOneReplyPerConn(t, &dials)

	c := resp.NewClient(resp.Options{Addr: addr, PoolSize: 1})
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	got, err := c.Get(ctx, "k")
	if err != nil || got != "v" {
		t.Fatalf("first GET: got=%q err=%v", got, err)
	}

	// Let the FIN arrive while the connection is parked idle in the pool.
	time.Sleep(100 * time.Millisecond)

	got, err = c.Get(ctx, "k2")
	if err != nil {
		t.Fatalf("second GET on recycled-but-dead conn: got=%q err=%v (want a transparent retry on a fresh connection, not a hard failure)", got, err)
	}
	if got != "v" {
		t.Fatalf("second GET: got=%q, want %q", got, "v")
	}
	if n := dials.Load(); n != 2 {
		t.Fatalf("connections dialled = %d, want 2 (one stale, one retry)", n)
	}
}

// TestClientDoesNotRetryFreshConnection pins the other half of the rule: only a
// reused connection earns a retry. A command that fails on a connection it just
// dialled is reporting the peer's own failure, and retrying it would double
// every command against a broken server.
func TestClientDoesNotRetryFreshConnection(t *testing.T) {
	var dials atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			_ = nc.Close() // never reply: every attempt fails at the transport
		}
	}()

	c := resp.NewClient(resp.Options{Addr: ln.Addr().String(), PoolSize: 1})
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Get(context.Background(), "k"); err == nil {
		t.Fatal("want error from a server that never replies")
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("connections dialled = %d, want 1 (a freshly dialled connection is not retried)", n)
	}
}

// TestClientDoesNotRetryTimedOutCommand pins the third half of the rule: a
// reused connection whose command times out is not retried either. A dead idle
// socket fails with EOF or a reset on its first I/O; a timeout means the peer
// is alive but slow, and re-sending would double the load on a struggling
// cache and double the caller's wait.
func TestClientDoesNotRetryTimedOutCommand(t *testing.T) {
	var dials, commands atomic.Int64
	addr := serveFirstThenStall(t, &dials, &commands)

	c := resp.NewClient(resp.Options{Addr: addr, PoolSize: 1, ReadTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	if got, err := c.Get(ctx, "k"); err != nil || got != "v" {
		t.Fatalf("first GET: got=%q err=%v", got, err)
	}
	_, err := c.Get(ctx, "k2")
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("second GET on a stalled peer: err=%v, want a read deadline error", err)
	}
	if n := commands.Load(); n != 2 {
		t.Fatalf("commands received by the peer = %d, want 2 (a timed-out command is not re-sent)", n)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("connections dialled = %d, want 1 (no retry on a timeout)", n)
	}
}

// serveFirstThenStall starts a stub RESP server that answers the first command
// on each connection with the bulk string "v" and then reads, but never
// answers, every later command, holding the connection open.
func serveFirstThenStall(t *testing.T, dials, commands *atomic.Int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func() {
				defer func() { _ = nc.Close() }()
				br := bufio.NewReader(nc)
				for first := true; ; first = false {
					if err := readRESPArray(br); err != nil {
						return
					}
					commands.Add(1)
					if first {
						_, _ = nc.Write([]byte("$1\r\nv\r\n"))
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// serveOneReplyPerConn starts a stub RESP server that answers one command per
// connection with the bulk string "v" and then closes it, counting accepts.
func serveOneReplyPerConn(t *testing.T, dials *atomic.Int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func() {
				defer func() { _ = nc.Close() }()
				if err := readRESPArray(bufio.NewReader(nc)); err != nil {
					return
				}
				_, _ = nc.Write([]byte("$1\r\nv\r\n"))
			}()
		}
	}()
	return ln.Addr().String()
}

// readRESPArray reads and discards one RESP2 array-of-bulk-strings command,
// just enough to unblock the stub server's single write.
func readRESPArray(br *bufio.Reader) error {
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	n := 0
	for i := 1; i < len(line) && line[i] >= '0' && line[i] <= '9'; i++ {
		n = n*10 + int(line[i]-'0')
	}
	for range n {
		if _, err := br.ReadString('\n'); err != nil { // $len\r\n
			return err
		}
		if _, err := br.ReadString('\n'); err != nil { // payload\r\n
			return err
		}
	}
	return nil
}

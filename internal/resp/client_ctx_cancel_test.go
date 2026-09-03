package resp_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/resp"
)

// TestClientCtxCancelUnblocksReadWithNegativeTimeout pins the documented
// contract at the Options doc — "a negative [timeout] disables that deadline
// (only the caller ctx bounds the operation)" — for a caller ctx that carries
// no explicit deadline, only cancellation (the shape an errgroup or a
// client-disconnect produces).
//
// deadline() only ever looks at ctx.Deadline(), never at ctx.Done(). With the
// per-command timeouts disabled, deadline() returns the zero time, the socket
// deadline is cleared entirely, and the blocking read has nothing to unblock
// it on cancellation — the goroutine hangs forever holding a pool token that
// Close() does not reclaim.
func TestClientCtxCancelUnblocksReadWithNegativeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	connCh := make(chan net.Conn, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		// Drain whatever the client writes but never reply, simulating a
		// wedged Redis node that still accepts TCP but never answers.
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := nc.Read(buf); err != nil {
					return
				}
			}
		}()
		connCh <- nc
	}()

	c := resp.NewClient(resp.Options{Addr: ln.Addr().String(), ReadTimeout: -1, WriteTimeout: -1})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, "k")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error after ctx cancel, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return within 2s of ctx cancel: blocking read ignores ctx.Done() when ReadTimeout is disabled (-1)")
	}

	select {
	case nc := <-connCh:
		_ = nc.Close()
	default:
	}
}

// TestClientCtxCancelReleasesPoolToken pins the consequence of the hang: a
// cancelled command must give its pool token back, or PoolSize wedged calls
// lock the pool out permanently.
func TestClientCtxCancelReleasesPoolToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = nc.Close() }()
				buf := make([]byte, 4096)
				for {
					if _, err := nc.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()

	// PoolSize 1: the second command cannot even check out a connection until
	// the first has released its token.
	c := resp.NewClient(resp.Options{Addr: ln.Addr().String(), PoolSize: 1, ReadTimeout: -1, WriteTimeout: -1})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	first := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, "k")
		first <- err
	}()
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("want error after ctx cancel, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return within 2s of ctx cancel")
	}

	// The token must be free again: a command whose own ctx expires quickly
	// should fail on its own deadline, not block forever in getConn.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Get(ctx2, "k")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Get blocked: the cancelled command never released its pool token")
	}
}

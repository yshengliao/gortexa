package resp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/yshengliao/gortexa/internal/resp"
)

func newClient(t *testing.T) (*resp.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := resp.NewClient(resp.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestClientGetSetDel(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()

	if _, err := c.Get(ctx, "k"); !errors.Is(err, resp.ErrNil) {
		t.Fatalf("miss: got %v, want ErrNil", err)
	}
	if err := c.Set(ctx, "k", "v", 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil || got != "v" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := c.Del(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, resp.ErrNil) {
		t.Fatalf("after del: got %v, want ErrNil", err)
	}
}

func TestClientTTL(t *testing.T) {
	c, mr := newClient(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Minute)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, resp.ErrNil) {
		t.Fatalf("after expiry: got %v, want ErrNil", err)
	}
}

func TestClientBinaryValue(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	val := string([]byte{0x00, 0x01, 0xff, '\r', '\n', 0x7f})
	if err := c.Set(ctx, "b", val, 0); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "b")
	if err != nil || got != val {
		t.Fatalf("binary round-trip failed: %q vs %q, %v", got, val, err)
	}
}

func TestClientPing(t *testing.T) {
	c, _ := newClient(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientConcurrent(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	const n = 50
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			key := "k" + string(rune('a'+i%26))
			if err := c.Set(ctx, key, "v", 0); err != nil {
				errs <- err
				return
			}
			_, err := c.Get(ctx, key)
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent op %d: %v", i, err)
		}
	}
}

func TestClientClosedRejectsCommands(t *testing.T) {
	c, _ := newClient(t)
	_ = c.Close()
	if _, err := c.Get(context.Background(), "k"); err == nil {
		t.Fatal("want error after Close")
	}
	if err := c.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
}

// TestClientDeadlineNotStaleOnReuse pins that a deadline armed by one command's
// ctx does not persist on the pooled connection and fire on the next reuse.
// With per-command timeouts disabled (-1), command 1 sets a net.Conn deadline
// from its ctx; after that instant passes, command 2 (no ctx deadline) reuses
// the same connection and must not inherit the stale, already-expired deadline.
func TestClientDeadlineNotStaleOnReuse(t *testing.T) {
	mr := miniredis.RunT(t)
	c := resp.NewClient(resp.Options{Addr: mr.Addr(), ReadTimeout: -1, WriteTimeout: -1})
	t.Cleanup(func() { _ = c.Close() })

	ctx1, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	if err := c.Set(ctx1, "k", "v", 0); err != nil { // dials conn A, arms A's deadline to ~now+150ms
		t.Fatal(err)
	}
	cancel()
	time.Sleep(200 * time.Millisecond) // A's stale deadline is now in the past

	// Reuses conn A with no ctx deadline: must clear A's stale deadline, not fail.
	if got, err := c.Get(context.Background(), "k"); err != nil || got != "v" {
		t.Fatalf("reuse after stale deadline: got %q, %v", got, err)
	}
}

func TestClientUnreachable(t *testing.T) {
	c := resp.NewClient(resp.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second})
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Get(context.Background(), "k"); err == nil {
		t.Fatal("want dial error against a dead address")
	}
}

// TestClientNegativeDialTimeoutDials pins that DialTimeout: -1 means "disabled"
// (per the Options doc), not an already-expired dial deadline. Before the fix,
// net.Dialer{Timeout: -1} failed every dial with an immediate i/o timeout.
func TestClientNegativeDialTimeoutDials(t *testing.T) {
	mr := miniredis.RunT(t)
	c := resp.NewClient(resp.Options{Addr: mr.Addr(), DialTimeout: -1})
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Set(context.Background(), "k", "v", 0); err != nil {
		t.Fatalf("dial with disabled DialTimeout: %v", err)
	}
	if got, err := c.Get(context.Background(), "k"); err != nil || got != "v" {
		t.Fatalf("get after disabled-timeout dial: got %q, %v", got, err)
	}
}

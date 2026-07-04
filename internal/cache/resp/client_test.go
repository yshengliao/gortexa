package resp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/yshengliao/gortexa/internal/cache/resp"
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

func TestClientUnreachable(t *testing.T) {
	c := resp.NewClient(resp.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second})
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Get(context.Background(), "k"); err == nil {
		t.Fatal("want dial error against a dead address")
	}
}

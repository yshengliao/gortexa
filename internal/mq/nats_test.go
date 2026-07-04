//go:build integration

package mq_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"go.uber.org/goleak"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/mq"
)

func TestNATSPubSub(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: srv.ClientURL()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	ctx := context.Background()
	got := make(chan mq.Message, 1)
	if err := sub.Subscribe(ctx, "events", func(_ context.Context, m mq.Message) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := mq.Message{Key: []byte("k1"), Value: []byte("hello"), Headers: map[string]string{"trace": "abc"}}
	if err := pub.Publish(ctx, "events", want); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-got:
		if string(m.Value) != "hello" || string(m.Key) != "k1" || m.Headers["trace"] != "abc" {
			t.Fatalf("received %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

// TestNATSSubscribeNoGoroutineLeakAfterClose pins the fix for the per-Subscribe
// watcher leak: subscribing with a non-cancellable context (Background) and then
// closing the client must not leave a goroutine parked on ctx.Done() forever.
// Two independent guards catch a regression: Close blocks on wg.Wait(), so
// dropping the c.done case from the watcher's select would hang here; and goleak
// (baselined after connect via IgnoreCurrent) flags any surviving watcher.
func TestNATSSubscribeNoGoroutineLeakAfterClose(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: srv.ClientURL()})
	if err != nil {
		t.Fatal(err)
	}

	// Baseline once the connection's own goroutines exist, so only leaked
	// per-subscription watchers can trip VerifyNone.
	opts := []goleak.Option{goleak.IgnoreCurrent()}

	for i := 0; i < 16; i++ {
		if err := sub.Subscribe(context.Background(), "events", func(context.Context, mq.Message) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- pub.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung: a watcher goroutine never exited (missing c.done select case?)")
	}

	goleak.VerifyNone(t, opts...)
}

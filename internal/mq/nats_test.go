//go:build integration

package mq_test

import (
	"context"
	"fmt"
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
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

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

// TestNATSFanOut pins the cross-backend default (GroupID empty): every
// subscription receives every message.
func TestNATSFanOut(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: srv.ClientURL()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	ctx := context.Background()
	got1 := make(chan mq.Message, 1)
	got2 := make(chan mq.Message, 1)
	for _, ch := range []chan mq.Message{got1, got2} {
		ch := ch
		if err := sub.Subscribe(ctx, "events", func(_ context.Context, m mq.Message) error {
			ch <- m
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := pub.Publish(ctx, "events", mq.Message{Value: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	for i, ch := range []chan mq.Message{got1, got2} {
		select {
		case m := <-ch:
			if string(m.Value) != "hello" {
				t.Fatalf("subscriber %d received %+v", i+1, m)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("subscriber %d never received the fan-out message", i+1)
		}
	}
}

// TestNATSQueueGroupLoadBalance pins the non-empty GroupID semantics: the two
// subscriptions share a queue group, so each message is delivered to exactly
// one of them — no loss, no duplication.
func TestNATSQueueGroupLoadBalance(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: srv.ClientURL(), GroupID: "workers"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	ctx := context.Background()
	const n = 8
	got := make(chan string, 2*n)
	for i := 0; i < 2; i++ {
		if err := sub.Subscribe(ctx, "jobs", func(_ context.Context, m mq.Message) error {
			got <- string(m.Value)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < n; i++ {
		if err := pub.Publish(ctx, "jobs", mq.Message{Value: []byte(fmt.Sprintf("job-%d", i))}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	for len(seen) < n {
		select {
		case v := <-got:
			seen[v]++
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out: received %d/%d distinct messages", len(seen), n)
		}
	}
	// A duplicate would mean both queue-group members got the same message —
	// exactly the load-balance guarantee this test pins down.
	select {
	case v := <-got:
		t.Fatalf("message %q delivered more than once within the queue group", v)
	case <-time.After(500 * time.Millisecond):
	}
	for v, count := range seen {
		if count != 1 {
			t.Fatalf("message %q delivered %d times", v, count)
		}
	}
}

// TestNATSCloseWaitsForInflightHandler pins Close-vs-handler symmetry with the
// Kafka backend: a handler that is already running when Close is called must
// finish before Close returns (given an unexpired ctx), so shutdown never
// abandons work silently.
func TestNATSCloseWaitsForInflightHandler(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: srv.ClientURL()})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := sub.Subscribe(ctx, "events", func(context.Context, mq.Message) error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "events", mq.Message{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never invoked")
	}

	closed := make(chan error, 1)
	go func() { closed <- pub.Close(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a handler was still in flight")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the handler finished")
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
	go func() { done <- pub.Close(context.Background()) }()
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

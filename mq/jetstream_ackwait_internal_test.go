//go:build integration

package mq

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"

	"github.com/yshengliao/gortexa/config"
)

// TestJetStreamSlowHandlerIsNotRedelivered pins AckWait handling. The consumer
// set no AckWait (server default 30s) and never signalled progress, so a
// handler slower than AckWait had its message redelivered while still running
// and, because Consume dispatches sequentially, processed again right after
// the first attempt acked — up to MaxDeliver times. The consumer now sets
// AckWait explicitly and sends InProgress while the handler runs. The test
// shortens AckWait to 2s and runs a 3s handler: exactly one invocation.
func TestJetStreamSlowHandlerIsNotRedelivered(t *testing.T) {
	saved := jsAckWait
	jsAckWait = 2 * time.Second
	t.Cleanup(func() { jsAckWait = saved })

	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	pub, sub, err := NewJetStream(config.MQConfig{Driver: "jetstream", URL: config.Secret(srv.ClientURL()), GroupID: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	var calls atomic.Int32
	var sawDeadline atomic.Bool
	done := make(chan struct{}, 4)
	if err := sub.Subscribe(context.Background(), "slow", func(ctx context.Context, _ Message) error {
		calls.Add(1)
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		time.Sleep(3 * time.Second) // > AckWait, < handler timeout
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), "slow", Message{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never finished")
	}
	// Give a wrongly-redelivered copy time to arrive.
	time.Sleep(3 * time.Second)
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler invoked %d times for one message, want 1 (AckWait expired mid-handler)", n)
	}
	if !sawDeadline.Load() {
		t.Fatal("handler ctx carries no deadline; a hung handler would hold its slot forever")
	}
}

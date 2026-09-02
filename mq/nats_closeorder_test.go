//go:build integration

package mq_test

import (
	"context"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/mq"
)

// TestNATSCloseKeepsConnectionAliveForInflightHandler pins Close's ordering
// against an in-flight handler's own use of the connection: a handler that is
// still running when Close is called must be able to complete its normal
// work (here, publishing a follow-up message) before the connection goes
// away underneath it. mq.go's doc comment says Subscriber's Close semantics
// match Publisher's, and jetstream.go closes its connection only after
// hwg.Wait() for exactly this reason (see its "Ack can still reach the
// server" comment) — core NATS's Close must honour the same ordering.
func TestNATSCloseKeepsConnectionAliveForInflightHandler(t *testing.T) {
	pub, sub, err := mq.NewNATS(config.MQConfig{URL: config.Secret(natsURL(t))})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	publishErr := make(chan error, 1)
	if err := sub.Subscribe(ctx, "events", func(context.Context, mq.Message) error {
		close(entered)
		<-release // block here until the test has called Close
		// Simulate the ordinary pipeline behaviour: a handler mid-flight
		// during shutdown still tries to emit its follow-up event.
		publishErr <- pub.Publish(context.Background(), "followup", mq.Message{Value: []byte("y")})
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

	// Give Close time to run its teardown up to the point where it blocks on
	// hwg.Wait() (the handler is still parked on <-release), so that if Close
	// tears down the connection before waiting, that teardown has already
	// happened by the time we release the handler below.
	time.Sleep(300 * time.Millisecond)
	close(release)

	select {
	case err := <-publishErr:
		if err != nil {
			t.Fatalf("in-flight handler's Publish during Close = %v, want nil (connection must stay usable until the handler finishes)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight handler's Publish never returned")
	}

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the handler finished")
	}
}

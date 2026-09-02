//go:build integration

package mq_test

import (
	"context"
	"testing"
	"time"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/mq"
)

// TestJetStreamCallerMsgIdHeaderRejected pins the reserved-namespace guard on
// the driver that acts on it. A caller header in the broker's "Nats-*"
// namespace is not inert metadata: natsWireMsg copies caller headers verbatim,
// so a caller-supplied Nats-Msg-Id drove JetStream's server-side
// de-duplication — a second message carrying the same value was discarded
// inside the stream's duplicate window while Publish still returned nil,
// reporting durable success for a message that was never stored. It is
// rejected before the wire now, and ordinary headers still pass.
func TestJetStreamCallerMsgIdHeaderRejected(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const topic = "dup"
	received := make(chan string, 2)
	if err := sub.Subscribe(ctx, topic, func(_ context.Context, m mq.Message) error {
		received <- string(m.Value)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Give the ephemeral consumer a moment to attach before publishing.
	time.Sleep(200 * time.Millisecond)

	err := pub.Publish(ctx, topic, mq.Message{Value: []byte("first"), Headers: map[string]string{"Nats-Msg-Id": "same-id"}})
	if err == nil {
		t.Fatal("Publish with a caller Nats-Msg-Id header must fail")
	}
	if !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("category = %v, want InvalidArgument", err)
	}

	// The guard is scoped to the reserved namespace: two distinct messages
	// carrying only ordinary headers are both stored and both delivered.
	for _, v := range []string{"first", "second"} {
		if err := pub.Publish(ctx, topic, mq.Message{Value: []byte(v), Headers: map[string]string{"trace": "abc"}}); err != nil {
			t.Fatalf("publish %q: %v", v, err)
		}
	}
	var got []string
	timeout := time.After(3 * time.Second)
	for range 2 {
		select {
		case v := <-received:
			got = append(got, v)
		case <-timeout:
			t.Fatalf("only %d/2 messages delivered (got %v)", len(got), got)
		}
	}
}

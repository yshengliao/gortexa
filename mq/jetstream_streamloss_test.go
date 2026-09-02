//go:build integration

package mq_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/yshengliao/gortexa/mq"
)

// TestJetStreamRecreatesStreamAfterExternalDeletion verifies that once a
// stream disappears out from under a long-running client (disaster recovery,
// `nats stream rm`, a JetStream domain reset), a subsequent Publish can heal
// by recreating it — rather than being permanently short-circuited by
// ensureStream's cache into a retryable-but-never-succeeding error.
func TestJetStreamRecreatesStreamAfterExternalDeletion(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, _ := newJetStreamClient(t, url, "")
	ctx := context.Background()

	const topic = "orders"

	// First publish creates the stream and caches "ensured".
	if err := pub.Publish(ctx, topic, mq.Message{Value: []byte("first")}); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Out-of-band: the stream is deleted (simulating cluster rebuild / nats
	// stream rm), independent of this client's cache.
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := js.DeleteStream(ctx, "gortexa_orders"); err != nil {
		t.Fatalf("delete stream: %v", err)
	}

	// The client's cache still believes the stream is ensured. A healthy
	// implementation notices the publish failure and heals on a later
	// attempt by recreating the stream; the defective implementation skips
	// ensureStream forever and every subsequent Publish fails.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = pub.Publish(ctx, topic, mq.Message{Value: []byte("after-deletion")})
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("publish never recovered after external stream deletion: %v", lastErr)
	}
}

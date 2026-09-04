//go:build integration

package mq_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/mq"
)

// TestNATSNoRedeliveryOnHandlerError pins core NATS's at-most-once contract:
// a handler error drops the message, there is no redelivery, and the loss is
// silent to the publisher.
//
// The asymmetry this closes was backwards. The *added* guarantee had a test —
// TestJetStreamRedeliveryOnHandlerError asserts JetStream redelivers — while
// the baseline it is contrasted against had none, even though the untested half
// is the one a developer relies on when deciding whether a handler must be
// idempotent. mq.go, nats.go, config.go and the README all state at-most-once
// in prose; nothing asserted it, so a change that silently introduced
// redelivery on this path would have broken no test.
func TestNATSNoRedeliveryOnHandlerError(t *testing.T) {
	pub, sub, err := mq.NewNATS(config.MQConfig{URL: config.Secret(natsURL(t))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })
	t.Cleanup(func() { _ = sub.Close(context.Background()) })

	ctx := context.Background()
	var mu sync.Mutex
	var failed, delivered []string

	if err := sub.Subscribe(ctx, "atmostonce", func(_ context.Context, m mq.Message) error {
		mu.Lock()
		defer mu.Unlock()
		body := string(m.Value)
		if body == "poison" {
			failed = append(failed, body)
			return errors.New("handler rejects this one")
		}
		delivered = append(delivered, body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The failing message first, then a marker. Once the marker arrives, the
	// broker has had its chance to redeliver the first and demonstrably did not.
	if err := pub.Publish(ctx, "atmostonce", mq.Message{Value: []byte("poison")}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "atmostonce", mq.Message{Value: []byte("marker")}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		gotMarker := len(delivered) > 0
		mu.Unlock()
		if gotMarker {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the marker message never arrived")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Give any redelivery a window well past JetStream's own ~1s NakWithDelay
	// pacing, so a redelivery would land inside it if this driver had one.
	time.Sleep(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("the failing handler ran %d times, want exactly 1: core NATS must not "+
			"redeliver after a handler error", len(failed))
	}
	if len(delivered) != 1 || delivered[0] != "marker" {
		t.Fatalf("delivered = %v, want exactly [marker]: the rejected message must be "+
			"dropped, not requeued behind it", delivered)
	}
}

// The publisher is told nothing: at-most-once means the loss is invisible from
// the publish side, which is precisely why a non-idempotent handler is unsafe
// only on the *other* driver and why this contract is worth pinning.
func TestNATSPublishSucceedsEvenWhenTheHandlerRejects(t *testing.T) {
	pub, sub, err := mq.NewNATS(config.MQConfig{URL: config.Secret(natsURL(t))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })
	t.Cleanup(func() { _ = sub.Close(context.Background()) })

	ctx := context.Background()
	ran := make(chan struct{}, 1)
	if err := sub.Subscribe(ctx, "atmostonce-pub", func(context.Context, mq.Message) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return errors.New("always fails")
	}); err != nil {
		t.Fatal(err)
	}

	if err := pub.Publish(ctx, "atmostonce-pub", mq.Message{Value: []byte("x")}); err != nil {
		t.Fatalf("Publish must succeed regardless of the handler's outcome: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}
}

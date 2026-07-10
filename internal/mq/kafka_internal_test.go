//go:build integration

package mq

import (
	"context"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/yshengliao/gortexa/internal/config"
)

// TestNewKafkaRequiresAllAcks pins the durability fix: the Writer must wait for
// the full in-sync replica set, not the struct-literal zero value RequireNone
// (fire-and-forget) that would let Publish report success before the broker
// stored the message. NewKafka does no I/O, so this needs no live broker.
func TestNewKafkaRequiresAllAcks(t *testing.T) {
	pub, _, err := NewKafka(config.MQConfig{URL: "127.0.0.1:9092"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := pub.(*kafkaClient)
	if !ok {
		t.Fatalf("NewKafka returned %T, want *kafkaClient", pub)
	}
	if c.writer.RequiredAcks != kafka.RequireAll {
		t.Fatalf("RequiredAcks = %v, want RequireAll (fire-and-forget would drop messages silently)", c.writer.RequiredAcks)
	}
}

// TestKafkaReaderSetSelfRemoves pins the reader-set fix: each subscription's
// goroutine removes its own reader from c.readers when its context ends, so the
// set tracks live readers only instead of growing for the client's lifetime
// (mirrors natsClient.subs). kafka.NewReader is lazy, so this needs no live
// broker — cancelling the subscription context unblocks FetchMessage.
func TestKafkaReaderSetSelfRemoves(t *testing.T) {
	pub, sub, err := NewKafka(config.MQConfig{URL: "127.0.0.1:9092"})
	if err != nil {
		t.Fatal(err)
	}
	c := sub.(*kafkaClient)
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	const n = 5
	cancels := make([]context.CancelFunc, 0, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		if err := sub.Subscribe(ctx, "retention", func(context.Context, Message) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	// The reader is inserted synchronously before its goroutine starts, so the
	// set holds exactly n entries right after subscribing.
	c.mu.Lock()
	got := len(c.readers)
	c.mu.Unlock()
	if got != n {
		t.Fatalf("readers after %d subscribes = %d, want %d", n, got, n)
	}

	for _, cancel := range cancels {
		cancel()
	}

	// Each cancelled subscription's goroutine closes its reader and deletes its
	// entry; teardown is asynchronous, so poll until the set drains.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		remaining := len(c.readers)
		c.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readers not released after cancel: %d still retained", remaining)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

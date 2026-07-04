//go:build integration

package mq_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/goleak"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/mq"
)

// kafkaBroker resolves the broker address for the Kafka integration tests. If
// KAFKA_BROKER is set (CI's hard guarantee), it is used as-is and a connection
// failure fails the test. Otherwise it falls back to the docker-compose default
// and skips when nothing is listening, so local `make test-integration` runs
// stay green without a broker.
func kafkaBroker(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("KAFKA_BROKER"); b != "" {
		return b
	}
	const addr = "127.0.0.1:9092"
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("kafka not reachable at %s: %v (start it with: docker compose -f deploy/docker-compose.yaml up -d kafka)", addr, err)
	}
	_ = conn.Close()
	return addr
}

// createKafkaTopic pre-creates topic before subscribing: kafka-go's Writer does
// not auto-create topics by default. DialLeader triggers broker-side
// auto-creation; retry for up to ~10s to ride out the metadata-propagation
// window on a freshly created topic.
func createKafkaTopic(t *testing.T, broker, topic string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := kafka.DialLeader(context.Background(), "tcp", broker, topic, 0)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("create topic %q: %v", topic, lastErr)
}

func TestKafkaPubSub(t *testing.T) {
	broker := kafkaBroker(t)
	topic := fmt.Sprintf("events-%d", time.Now().UnixNano())
	createKafkaTopic(t, broker, topic)

	pub, sub, err := mq.NewKafka(config.MQConfig{URL: broker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	ctx := context.Background()
	got := make(chan mq.Message, 1)
	if err := sub.Subscribe(ctx, topic, func(_ context.Context, m mq.Message) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := mq.Message{Key: []byte("k1"), Value: []byte("hello"), Headers: map[string]string{"trace": "abc"}}
	if err := pub.Publish(ctx, topic, want); err != nil {
		t.Fatal(err)
	}

	// Consumer-group join + rebalance is slow, especially on a first-run CI
	// broker, so this timeout is longer than the NATS equivalent.
	select {
	case m := <-got:
		if string(m.Value) != "hello" || string(m.Key) != "k1" || m.Headers["trace"] != "abc" {
			t.Fatalf("received %+v", m)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

// TestKafkaSubscribeNoGoroutineLeakAfterClose mirrors the NATS goroutine-hygiene
// test: subscribing with a non-cancellable context (Background) and then
// closing the client must not leave a reader goroutine behind. Two independent
// guards catch a regression: Close blocks on wg.Wait(), so a dropped exit path
// in the reader loop would hang here; and goleak (baselined after NewKafka via
// IgnoreCurrent, since kafka-go spawns its own background goroutines) flags any
// surviving reader.
func TestKafkaSubscribeNoGoroutineLeakAfterClose(t *testing.T) {
	broker := kafkaBroker(t)
	topic := fmt.Sprintf("events-%d", time.Now().UnixNano())
	createKafkaTopic(t, broker, topic)

	pub, sub, err := mq.NewKafka(config.MQConfig{URL: broker})
	if err != nil {
		t.Fatal(err)
	}

	// Baseline once the client's own goroutines exist, so only leaked reader
	// goroutines can trip VerifyNone.
	opts := []goleak.Option{goleak.IgnoreCurrent()}

	for i := 0; i < 8; i++ {
		if err := sub.Subscribe(context.Background(), topic, func(context.Context, mq.Message) error { return nil }); err != nil {
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
	case <-time.After(15 * time.Second):
		t.Fatal("Close hung: a reader goroutine never exited (missing wg.Done on some exit path?)")
	}

	// kafka-go readers may leave short-lived internal goroutines mid-teardown;
	// retry briefly before failing so the check isn't flaky on their exit timing.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := goleak.Find(opts...); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

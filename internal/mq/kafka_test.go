//go:build integration

package mq_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
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

// createKafkaTopicPartitions creates topic with an explicit partition count —
// DialLeader auto-creation always yields the broker default (one partition),
// which cannot exercise load-balancing across group members.
func createKafkaTopicPartitions(t *testing.T, broker, topic string, partitions int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := kafka.Dial("tcp", broker)
		if err == nil {
			err = conn.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: partitions, ReplicationFactor: 1})
			_ = conn.Close()
			if err == nil {
				return
			}
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("create topic %q: %v", topic, lastErr)
}

// TestKafkaFanOut pins the cross-backend default (GroupID empty): each
// subscription gets its own throwaway consumer group, so every subscriber sees
// the published messages. The topic is auto-created with a single partition,
// which makes the test sharp — under the old shared-group behaviour one
// subscriber would own the sole partition and starve the other forever.
func TestKafkaFanOut(t *testing.T) {
	broker := kafkaBroker(t)
	topic := fmt.Sprintf("events-%d", time.Now().UnixNano())
	createKafkaTopic(t, broker, topic)

	pub, sub, err := mq.NewKafka(config.MQConfig{URL: broker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	ctx := context.Background()
	got1 := make(chan string, 256)
	got2 := make(chan string, 256)
	for _, ch := range []chan string{got1, got2} {
		ch := ch
		if err := sub.Subscribe(ctx, topic, func(_ context.Context, m mq.Message) error {
			ch <- string(m.Value)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Fan-out readers start at LastOffset, so anything published before a
	// reader's group join is invisible to it. Keep publishing until both
	// subscribers have received something; publish errors during the fresh-topic
	// metadata window are part of the same retry loop.
	deadline := time.Now().Add(60 * time.Second)
	var ok1, ok2 bool
	for i := 0; !(ok1 && ok2); i++ {
		if time.Now().After(deadline) {
			t.Fatalf("fan-out: subscriber1 received=%v subscriber2 received=%v after 60s", ok1, ok2)
		}
		_ = pub.Publish(ctx, topic, mq.Message{Value: []byte(fmt.Sprintf("m-%d", i))})
		select {
		case <-got1:
			ok1 = true
		default:
		}
		select {
		case <-got2:
			ok2 = true
		default:
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestKafkaGroupLoadBalance pins the non-empty GroupID semantics: the two
// subscriptions join one consumer group over a two-partition topic, so the
// group splits the stream. Kafka is at-least-once within a group (a rebalance
// can redeliver an uncommitted message), so the invariants tested are the
// flake-free pair: no message is lost, and the stream is genuinely split — a
// fan-out regression would deliver every message to both subscribers (2n
// receptions) and fail the total bound.
func TestKafkaGroupLoadBalance(t *testing.T) {
	broker := kafkaBroker(t)
	topic := fmt.Sprintf("events-lb-%d", time.Now().UnixNano())
	createKafkaTopicPartitions(t, broker, topic, 2)

	group := fmt.Sprintf("lb-%d", time.Now().UnixNano())
	pub, sub, err := mq.NewKafka(config.MQConfig{URL: broker, GroupID: group})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	ctx := context.Background()
	got := make(chan string, 256)
	for i := 0; i < 2; i++ {
		if err := sub.Subscribe(ctx, topic, func(_ context.Context, m mq.Message) error {
			got <- string(m.Value)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Probe until the group consumes something: proves topic metadata has
	// propagated and group assignment completed. Probes are excluded from the
	// assertions below, so probe duplicates from join-time rebalancing are
	// harmless.
	probeDeadline := time.Now().Add(60 * time.Second)
	for probed := false; !probed; {
		if time.Now().After(probeDeadline) {
			t.Fatal("group never consumed a probe message within 60s")
		}
		_ = pub.Publish(ctx, topic, mq.Message{Value: []byte("probe")})
		select {
		case v := <-got:
			probed = v == "probe"
		default:
		}
		time.Sleep(250 * time.Millisecond)
	}
	// Let the second member's join/rebalance settle before the counted batch,
	// shrinking the redelivery window to near zero.
	time.Sleep(3 * time.Second)
	for len(got) > 0 {
		<-got // drain residual probes
	}

	const n = 8
	for i := 0; i < n; i++ {
		if err := pub.Publish(ctx, topic, mq.Message{Value: []byte(fmt.Sprintf("final-%d", i))}); err != nil {
			t.Fatalf("publish final-%d: %v", i, err)
		}
	}

	seen := map[string]int{}
	timeout := time.After(30 * time.Second)
	for len(seen) < n {
		select {
		case v := <-got:
			if strings.HasPrefix(v, "final-") {
				seen[v]++
			}
		case <-timeout:
			t.Fatalf("timed out: received %d/%d distinct messages", len(seen), n)
		}
	}
	silence := time.After(1 * time.Second)
	for draining := true; draining; {
		select {
		case v := <-got:
			if strings.HasPrefix(v, "final-") {
				seen[v]++
			}
		case <-silence:
			draining = false
		}
	}
	total := 0
	for _, count := range seen {
		total += count
	}
	if total >= 2*n {
		t.Fatalf("stream was not split: %d receptions of %d messages (fan-out regression)", total, n)
	}
}

func TestKafkaPubSub(t *testing.T) {
	broker := kafkaBroker(t)
	topic := fmt.Sprintf("events-%d", time.Now().UnixNano())
	createKafkaTopic(t, broker, topic)

	pub, sub, err := mq.NewKafka(config.MQConfig{URL: broker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	ctx := context.Background()
	// Buffered past the duplicate deliveries from the publish loop below, so the
	// handler can never block on send and wedge the reader goroutine (which
	// would in turn hang Close's wg.Wait in Cleanup).
	got := make(chan mq.Message, 256)
	if err := sub.Subscribe(ctx, topic, func(_ context.Context, m mq.Message) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Default (fan-out) subscriptions start at LastOffset: a message published
	// before the reader's group join completes is invisible to it. Publish until
	// received — this also rides out the fresh-topic metadata window, where the
	// first publishes fail with Unknown Topic Or Partition. Group join is slow on
	// a first-run CI broker, hence the generous deadline.
	want := mq.Message{Key: []byte("k1"), Value: []byte("hello"), Headers: map[string]string{"trace": "abc"}}
	deadline := time.Now().Add(60 * time.Second)
	var lastPubErr error
	for {
		if err := pub.Publish(ctx, topic, want); err != nil {
			lastPubErr = err
		}
		select {
		case m := <-got:
			if string(m.Value) != "hello" || string(m.Key) != "k1" || m.Headers["trace"] != "abc" {
				t.Fatalf("received %+v", m)
			}
			return
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for message (last publish error: %v)", lastPubErr)
		}
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
	go func() { done <- pub.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(30 * time.Second):
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

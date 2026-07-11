//go:build integration

package mq_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/goleak"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/mq"
)

// jetStreamServer resolves the server for the JetStream integration tests. If
// JETSTREAM_URL is set (e.g. a docker-compose broker started with -js) it is
// used as-is — a connection failure then fails the test, never skips — and the
// returned *server.Server is nil. Otherwise an embedded nats-server with
// JetStream enabled (file store under t.TempDir()) is started and torn down
// with the test. NATS_URL is deliberately not honoured here: an external core
// server may not have JetStream enabled.
func jetStreamServer(t *testing.T) (string, *server.Server) {
	t.Helper()
	if u := os.Getenv("JETSTREAM_URL"); u != "" {
		return u, nil
	}
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL(), srv
}

// newJetStreamClient builds the client through mq.New so the factory's
// "jetstream" case is exercised, not just NewJetStream directly.
func newJetStreamClient(t *testing.T, url, groupID string) (mq.Publisher, mq.Subscriber) {
	t.Helper()
	pub, sub, err := mq.New(config.MQConfig{Driver: "jetstream", URL: config.Secret(url), GroupID: groupID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })
	return pub, sub
}

func TestJetStreamPubSub(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

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
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

// TestJetStreamFanOut pins the cross-driver default (GroupID empty): every
// subscription receives every message.
func TestJetStreamFanOut(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	ctx := context.Background()
	got1 := make(chan mq.Message, 1)
	got2 := make(chan mq.Message, 1)
	for _, ch := range []chan mq.Message{got1, got2} {
		ch := ch
		if err := sub.Subscribe(ctx, "fanout", func(_ context.Context, m mq.Message) error {
			ch <- m
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := pub.Publish(ctx, "fanout", mq.Message{Value: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	for i, ch := range []chan mq.Message{got1, got2} {
		select {
		case m := <-ch:
			if string(m.Value) != "hello" {
				t.Fatalf("subscriber %d received %+v", i+1, m)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("subscriber %d never received the fan-out message", i+1)
		}
	}
}

// TestJetStreamQueueGroupLoadBalance pins the non-empty GroupID semantics: the
// two subscriptions share one durable consumer, so each message is delivered
// to exactly one of them — no loss, no duplication.
func TestJetStreamQueueGroupLoadBalance(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "workers")

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
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out: received %d/%d distinct messages", len(seen), n)
		}
	}
	// A duplicate would mean the durable consumer delivered the same message
	// to both members — exactly the load-balance guarantee this test pins.
	select {
	case v := <-got:
		t.Fatalf("message %q delivered more than once within the group", v)
	case <-time.After(500 * time.Millisecond):
	}
	for v, count := range seen {
		if count != 1 {
			t.Fatalf("message %q delivered %d times", v, count)
		}
	}
}

// TestJetStreamRedeliveryOnHandlerError pins the at-least-once property the
// JetStream driver adds over core NATS: a handler error negative-acks the
// message and the server redelivers it after jsNakDelay.
func TestJetStreamRedeliveryOnHandlerError(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "retry-workers")

	ctx := context.Background()
	var mu sync.Mutex
	var calls []time.Time
	done := make(chan struct{})
	if err := sub.Subscribe(ctx, "retry", func(_ context.Context, _ mq.Message) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, time.Now())
		if len(calls) == 1 {
			return errors.New("first attempt fails")
		}
		if len(calls) == 2 {
			close(done)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := pub.Publish(ctx, "retry", mq.Message{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("message was never redelivered after the handler error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("handler invoked %d times, want 2 (fail, then redelivered success)", len(calls))
	}
	// NakWithDelay(jsNakDelay) paces the redelivery; generous slack to stay
	// robust on a loaded runner.
	if gap := calls[1].Sub(calls[0]); gap < 500*time.Millisecond {
		t.Fatalf("redelivery arrived after %v, want the ~1s NakWithDelay pacing", gap)
	}
}

// TestJetStreamDeliverNewIgnoresHistory pins the DeliverNewPolicy half of the
// package contract: a subscription receives only messages published AFTER it
// subscribed — pre-existing stream history is not replayed. (Dropping the
// DeliverPolicy from the consumer config would default to DeliverAll and
// replay history; every other test subscribes before publishing, so only this
// test catches that mutation.)
func TestJetStreamDeliverNewIgnoresHistory(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	ctx := context.Background()
	// Publish before any subscription exists: the stream stores it as history.
	if err := pub.Publish(ctx, "history", mq.Message{Value: []byte("old")}); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 2)
	if err := sub.Subscribe(ctx, "history", func(_ context.Context, m mq.Message) error {
		got <- string(m.Value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "history", mq.Message{Value: []byte("new")}); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-got:
		if v != "new" {
			t.Fatalf("first delivery = %q, want only the post-subscribe message", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the post-subscribe message")
	}
	select {
	case v := <-got:
		t.Fatalf("history message %q replayed despite DeliverNew", v)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestJetStreamNotEnabledFailsLoud pins the AccountInfo probe: constructing
// the client against a server WITHOUT JetStream must fail within the probe
// timeout as FailedPrecondition, not surface on first Publish/Subscribe.
func TestJetStreamNotEnabledFailsLoud(t *testing.T) {
	opts := natsserver.DefaultTestOptions
	opts.Port = -1 // plain core server: no JetStream
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	pub, sub, err := mq.New(config.MQConfig{Driver: "jetstream", URL: config.Secret(srv.ClientURL())})
	if err == nil {
		_ = pub.Close(context.Background())
		t.Fatal("New must fail against a server without JetStream")
	}
	if pub != nil || sub != nil {
		t.Errorf("pub/sub should be nil on error, got %v, %v", pub, sub)
	}
	if !apperr.Is(err, apperr.CatFailedPrecondition) {
		t.Fatalf("category = %v, want FailedPrecondition", err)
	}
}

// TestJetStreamAdoptsOperatorStream pins the adoption contract: a pre-created
// stream bearing the framework-derived name and covering the topic is used
// as-is — its retention config is never rewritten.
func TestJetStreamAdoptsOperatorStream(t *testing.T) {
	url, _ := jetStreamServer(t)

	// Operator pre-creates the stream with retention that differs from the
	// framework default (24h).
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	const operatorMaxAge = time.Hour
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "gortexa_adopt",
		Subjects: []string{"adopt"},
		MaxAge:   operatorMaxAge,
	}); err != nil {
		t.Fatal(err)
	}

	pub, sub := newJetStreamClient(t, url, "")
	ctx := context.Background()
	got := make(chan mq.Message, 1)
	if err := sub.Subscribe(ctx, "adopt", func(_ context.Context, m mq.Message) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "adopt", mq.Message{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("message never delivered through the adopted stream")
	}

	info, err := js.Stream(context.Background(), "gortexa_adopt")
	if err != nil {
		t.Fatal(err)
	}
	if age := info.CachedInfo().Config.MaxAge; age != operatorMaxAge {
		t.Fatalf("operator stream MaxAge rewritten to %v, want %v untouched", age, operatorMaxAge)
	}
}

// TestJetStreamStreamMismatchFailsLoud pins the coverage check: a stream that
// bears the derived name but does not cover the topic is a FailedPrecondition,
// never silently mis-routed.
func TestJetStreamStreamMismatchFailsLoud(t *testing.T) {
	url, _ := jetStreamServer(t)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     "gortexa_blocked",
		Subjects: []string{"something-else"},
	}); err != nil {
		t.Fatal(err)
	}

	pub, _ := newJetStreamClient(t, url, "")
	err = pub.Publish(context.Background(), "blocked", mq.Message{Value: []byte("x")})
	if err == nil {
		t.Fatal("Publish must fail when the named stream does not cover the topic")
	}
	if !apperr.Is(err, apperr.CatFailedPrecondition) {
		t.Fatalf("category = %v, want FailedPrecondition", err)
	}
}

// TestJetStreamReservedHeaderRejected pins reserved-header parity with the
// core-NATS driver: the wire mapping is shared, so the same header name is
// rejected before anything reaches the server.
func TestJetStreamReservedHeaderRejected(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	ctx := context.Background()
	got := make(chan mq.Message, 1)
	if err := sub.Subscribe(ctx, "reserved", func(_ context.Context, m mq.Message) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := pub.Publish(ctx, "reserved", mq.Message{Value: []byte("x"), Headers: map[string]string{"gortexa-key": "boom"}})
	if err == nil {
		t.Fatal("Publish with the reserved header must fail")
	}
	if !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("category = %v, want InvalidArgument", err)
	}
	select {
	case m := <-got:
		t.Fatalf("rejected message was delivered: %+v", m)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestJetStreamCloseWaitsForInflightHandler pins Close-vs-handler ordering: a
// handler already running when Close is called must finish before Close
// returns (given an unexpired ctx), so shutdown never abandons work silently.
func TestJetStreamCloseWaitsForInflightHandler(t *testing.T) {
	url, _ := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := sub.Subscribe(ctx, "inflight", func(context.Context, mq.Message) error {
		close(entered)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "inflight", mq.Message{Value: []byte("x")}); err != nil {
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

// TestJetStreamSubscribeNoGoroutineLeakAfterClose mirrors the core-NATS leak
// test: subscribing with a non-cancellable context (Background) and closing
// the client must not leave a goroutine parked forever. The embedded server
// spawns its own per-consumer goroutines after the goleak baseline, so it is
// shut down (and waited for) before VerifyNone — the check then only sees
// client-side goroutines.
func TestJetStreamSubscribeNoGoroutineLeakAfterClose(t *testing.T) {
	url, srv := jetStreamServer(t)
	pub, sub := newJetStreamClient(t, url, "")

	// Baseline once the connection's own goroutines exist, so only leaked
	// client goroutines can trip VerifyNone.
	opts := []goleak.Option{goleak.IgnoreCurrent()}

	for i := 0; i < 16; i++ {
		if err := sub.Subscribe(context.Background(), "leak", func(context.Context, mq.Message) error { return nil }); err != nil {
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
		t.Fatal("Close hung: a watcher goroutine never exited")
	}

	if srv != nil {
		srv.Shutdown()
		srv.WaitForShutdown()
	}
	goleak.VerifyNone(t, opts...)
}

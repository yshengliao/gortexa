//go:build integration

package mq_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"

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
	t.Cleanup(func() { _ = pub.Close() })

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

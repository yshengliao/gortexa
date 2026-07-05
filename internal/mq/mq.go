// Package mq is a pluggable publish/subscribe abstraction with a NATS
// implementation (tested against an embedded server) and a Kafka implementation
// behind the integration build tag.
//
// Delivery semantics are uniform across backends, selected by
// config.MQConfig.GroupID: empty (the default) fans out — every subscription
// receives every message published after it subscribed; non-empty
// load-balances — subscriptions sharing the group split the stream (a NATS
// queue group or Kafka consumer group).
//
// Transport caveats callers must design for:
//   - Kafka subscriptions become live asynchronously: Subscribe returns before
//     the consumer-group join completes, so messages published in that window
//     are not delivered to the new subscription. NATS subscriptions are live
//     when Subscribe returns.
//   - Handler errors are not retried on either backend. On Kafka an explicit
//     group commits its offset only after the handler returns, so a crash
//     mid-handler is redelivered after restart (and duplicates are possible);
//     core NATS has no redelivery.
//
// config.MQConfig.URL accepts a comma-separated server list on both backends
// (the bootstrap-server convention).
package mq

import (
	"context"
	"errors"
	"strings"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// Message is a transport-neutral message.
type Message struct {
	Key     []byte
	Value   []byte
	Headers map[string]string
}

// Handler processes a received message.
type Handler func(ctx context.Context, m Message) error

// Publisher publishes messages to a topic. Close honours ctx as a shutdown
// budget: on expiry it returns early while the underlying teardown finishes in
// the background (see closeWithin).
type Publisher interface {
	Publish(ctx context.Context, topic string, m Message) error
	Close(ctx context.Context) error
}

// Subscriber subscribes a handler to a topic. Close semantics match Publisher.
type Subscriber interface {
	Subscribe(ctx context.Context, topic string, h Handler) error
	Close(ctx context.Context) error
}

// closeWithin runs fn (a blocking teardown) and honours ctx as an escape
// hatch: a broker that is gone can park a client close indefinitely, and a
// shutting-down caller needs its budget back. On expiry the wrapped ctx error
// is returned immediately while fn keeps running in its goroutine until the
// underlying close returns — the teardown is abandoned, not cancelled.
func closeWithin(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cat := apperr.CatDeadlineExceeded
		if errors.Is(ctx.Err(), context.Canceled) {
			cat = apperr.CatCanceled
		}
		return apperr.Wrap(cat, "mq close", ctx.Err())
	}
}

// splitBrokers parses MQConfig.URL as a comma-separated broker list for the
// Kafka backend. NATS instead passes the raw URL through — nats.Connect
// natively accepts a comma-separated server list.
func splitBrokers(url string) ([]string, error) {
	if url == "" {
		return nil, apperr.New(apperr.CatInvalidArgument, "mq.url (kafka brokers) required")
	}
	parts := strings.Split(url, ",")
	brokers := make([]string, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, apperr.New(apperr.CatInvalidArgument, "mq.url: empty broker entry in list")
		}
		brokers[i] = p
	}
	return brokers, nil
}

// New selects a message queue backend from config.
func New(cfg config.MQConfig) (Publisher, Subscriber, error) {
	switch cfg.Driver {
	case "nats", "":
		return NewNATS(cfg)
	case "kafka":
		return NewKafka(cfg)
	default:
		return nil, nil, apperr.New(apperr.CatInvalidArgument, "mq: unsupported driver: "+cfg.Driver)
	}
}

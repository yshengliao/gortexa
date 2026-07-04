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
// config.MQConfig.URL accepts a comma-separated server list on both backends
// (the bootstrap-server convention).
package mq

import (
	"context"
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

// Publisher publishes messages to a topic.
type Publisher interface {
	Publish(ctx context.Context, topic string, m Message) error
	Close() error
}

// Subscriber subscribes a handler to a topic.
type Subscriber interface {
	Subscribe(ctx context.Context, topic string, h Handler) error
	Close() error
}

// splitBrokers parses MQConfig.URL as a comma-separated broker list for the
// Kafka backend. NATS instead passes the raw URL through — nats.Connect
// natively accepts a comma-separated server list.
func splitBrokers(url string) ([]string, error) {
	if url == "" {
		return nil, apperr.New(apperr.CatInvalidArgument, "mq.url (kafka brokers) required")
	}
	parts := strings.Split(url, ",")
	brokers := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, apperr.New(apperr.CatInvalidArgument, "mq.url: empty broker entry in list")
		}
		brokers = append(brokers, p)
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

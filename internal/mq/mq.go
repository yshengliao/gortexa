// Package mq is a pluggable publish/subscribe abstraction with a NATS
// implementation (tested against an embedded server) and a Kafka implementation
// behind the integration build tag.
package mq

import "context"

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

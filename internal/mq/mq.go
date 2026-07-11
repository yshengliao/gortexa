// Package mq is a pluggable publish/subscribe abstraction backed by NATS
// (tested against an embedded server).
//
// Delivery semantics are selected by config.MQConfig.GroupID: empty (the
// default) fans out — every subscription receives every message published
// after it subscribed; non-empty load-balances — subscriptions sharing the
// group split the stream (a NATS queue group).
//
// Handler errors are not retried: core NATS has no redelivery, so a message
// whose handler failed is not seen again.
//
// config.MQConfig.URL accepts a comma-separated server list.
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
	Key   []byte
	Value []byte
	// Headers are caller-defined. The name in reservedKeyHeader is reserved by the
	// framework (the NATS backend uses it on the wire to carry Key), so publishing
	// a header by that name is rejected on every backend — see checkReservedHeaders.
	Headers map[string]string
}

// Handler processes a received message.
type Handler func(ctx context.Context, m Message) error

// reservedKeyHeader is the header name the NATS backend uses on the wire to carry
// Message.Key. It is reserved: a caller header by this name would otherwise
// silently overwrite Key on delivery, so Publish rejects it — see
// checkReservedHeaders.
const reservedKeyHeader = "gortexa-key"

// checkReservedHeaders rejects a caller header that collides with a framework
// reserved name. Called by every backend's Publish so a message never behaves
// differently when the backend changes.
func checkReservedHeaders(h map[string]string) error {
	if _, ok := h[reservedKeyHeader]; ok {
		return apperr.New(apperr.CatInvalidArgument, "mq: header "+reservedKeyHeader+" is reserved")
	}
	return nil
}

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

// validateServerList validates MQConfig.URL as a comma-separated server list.
// nats.Connect parses the list natively but silently drops a blank entry — a
// blank entry is a config typo, not a smaller cluster, so it fails loud here.
func validateServerList(url string) error {
	if url == "" {
		return apperr.New(apperr.CatInvalidArgument, "mq.url required")
	}
	for _, p := range strings.Split(url, ",") {
		if strings.TrimSpace(p) == "" {
			return apperr.New(apperr.CatInvalidArgument, "mq.url: empty server entry in list")
		}
	}
	return nil
}

// New selects a message queue backend from config.
func New(cfg config.MQConfig) (Publisher, Subscriber, error) {
	switch cfg.Driver {
	case "nats", "":
		return NewNATS(cfg)
	default:
		return nil, nil, apperr.New(apperr.CatInvalidArgument, "mq: unsupported driver: "+cfg.Driver)
	}
}

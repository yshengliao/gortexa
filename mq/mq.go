// Package mq is a pluggable publish/subscribe abstraction backed by NATS,
// with two drivers (both tested against an embedded server):
//
//   - "nats" (default): core NATS, at-most-once. Publish is fire-and-forget
//     plus flush; there is no redelivery, so a message whose handler failed
//     (or that arrived while no subscriber was up) is not seen again.
//   - "jetstream": NATS JetStream, at-least-once. Publish blocks on the
//     server's storage ack; a handler error negative-acks the message and the
//     server redelivers it, so handlers must be idempotent. Streams the
//     framework creates itself carry a 24h age cap; an operator can pre-create
//     a stream with different retention — it is adopted, never modified.
//
// Delivery semantics are uniform across drivers, selected by
// config.MQConfig.GroupID: empty (the default) fans out — every subscription
// receives every message published after it subscribed; non-empty
// load-balances — subscriptions sharing the group split the stream (a core
// NATS queue group / a shared durable JetStream consumer).
//
// JetStream caveats:
//   - A fan-out (GroupID empty) subscription is an ephemeral consumer; if its
//     client stalls past the server's inactive threshold the server reclaims
//     the consumer and the subscription goes silently dead.
//   - Topics must be literal subjects — wildcard tokens ("*", ">") are
//     rejected as InvalidArgument (core NATS supports wildcard subscriptions;
//     JetStream's per-topic streams cannot).
//   - A pre-created operator stream is adopted only when it bears the name
//     the framework derives ("gortexa_" + topic for a plain topic) and its
//     subjects cover the topic; anything else fails loud.
//
// config.MQConfig.URL accepts a comma-separated server list.
package mq

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
)

// Message is a transport-neutral message.
type Message struct {
	Key   []byte
	Value []byte
	// Headers are caller-defined except for two reserved names: the one in
	// reservedKeyHeader (the NATS backend uses it on the wire to carry Key) and
	// anything in the broker's natsReservedHeaderPrefix namespace. Publishing
	// either is rejected on every backend — see checkReservedHeaders.
	Headers map[string]string
}

// Handler processes a received message.
type Handler func(ctx context.Context, m Message) error

// reservedKeyHeader is the header name the NATS backend uses on the wire to carry
// Message.Key. It is reserved: a caller header by this name would otherwise
// silently overwrite Key on delivery, so Publish rejects it — see
// checkReservedHeaders.
const reservedKeyHeader = "gortexa-key"

// natsReservedHeaderPrefix is the namespace the broker itself interprets.
// These headers are not inert metadata: Nats-Msg-Id drives JetStream's
// server-side de-duplication (a message inside the stream's duplicate window
// is discarded while Publish still reports success), and
// Nats-Expected-Stream / Nats-Expected-Last-Sequence / Nats-Rollup likewise
// change what the server does with the message. The comparison is
// case-insensitive because nats.Header.Set does not canonicalise, so a caller
// picks the exact wire spelling.
const natsReservedHeaderPrefix = "nats-"

// checkReservedHeaders rejects a caller header that collides with a framework
// or broker reserved name. Called by every backend's Publish so a message never
// behaves differently when the backend changes — the same header that is inert
// on core NATS silently drops the message on JetStream.
func checkReservedHeaders(h map[string]string) error {
	if _, ok := h[reservedKeyHeader]; ok {
		return apperr.New(apperr.CatInvalidArgument, "mq: header "+reservedKeyHeader+" is reserved")
	}
	for k := range h {
		if len(k) >= len(natsReservedHeaderPrefix) &&
			strings.EqualFold(k[:len(natsReservedHeaderPrefix)], natsReservedHeaderPrefix) {
			return apperr.New(apperr.CatInvalidArgument, "mq: headers in the "+natsReservedHeaderPrefix+"* namespace are reserved by the broker")
		}
	}
	return nil
}

// safeInvoke runs a caller-supplied Handler under panic recovery, turning a
// panic into a handler error so the driver's normal failure path runs. A panic
// on a delivery goroutine has no caller frame to unwind into and aborts the
// whole process: on JetStream that also skips both Ack and Nak, so the poison
// message stays ack-pending and kills the replacement process on redelivery.
// Containment keeps the blast radius at one message, mirroring the Recovery
// interceptor on the gRPC surface and the MCP bridge. The panic value rides
// along as the wrapped cause, which apperr never serialises to a caller.
func safeInvoke(ctx context.Context, h Handler, m Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = apperr.Wrap(apperr.CatInternal, "mq: handler panic", fmt.Errorf("%v", r))
		}
	}()
	return h(ctx, m)
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
	for p := range strings.SplitSeq(url, ",") {
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
	case "jetstream":
		return NewJetStream(cfg)
	default:
		return nil, nil, apperr.New(apperr.CatInvalidArgument, "mq: unsupported driver: "+cfg.Driver)
	}
}

package mq

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

const natsKeyHeader = "gortexa-key"

type natsClient struct {
	conn    *nats.Conn
	groupID string
	mu      sync.Mutex
	// subs is keyed by subscription so an ended subscription can remove its own
	// entry, bounding the map to live subscriptions instead of growing forever.
	subs   map[*nats.Subscription]struct{}
	closed bool
	// done is closed once by Close so per-subscription watcher goroutines exit
	// even when the caller passed a non-cancellable context (e.g. Background).
	done chan struct{}
	// wg tracks watcher goroutines so Close returns only once they have all
	// exited — no goroutine outlives the client.
	wg sync.WaitGroup
	// hwg tracks in-flight handler invocations so Close's teardown waits for
	// them, matching the Kafka backend whose reader goroutine runs handlers
	// inline. Add happens under mu against the closed flag, so it can never
	// race a Wait that started at zero.
	hwg sync.WaitGroup
}

// NewNATS connects to NATS and returns a publisher and subscriber sharing one
// connection. cfg.URL may be a comma-separated server list — nats.Connect
// accepts one natively, so it is passed through unparsed.
func NewNATS(cfg config.MQConfig) (Publisher, Subscriber, error) {
	url := cfg.URL
	if url == "" {
		url = nats.DefaultURL
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.CatUnavailable, "nats connect", err)
	}
	c := &natsClient{
		conn:    conn,
		groupID: cfg.GroupID,
		subs:    make(map[*nats.Subscription]struct{}),
		done:    make(chan struct{}),
	}
	return c, c, nil
}

func (c *natsClient) Publish(ctx context.Context, topic string, m Message) error {
	msg := &nats.Msg{Subject: topic, Data: m.Value, Header: nats.Header{}}
	if len(m.Key) > 0 {
		msg.Header.Set(natsKeyHeader, string(m.Key))
	}
	for k, v := range m.Headers {
		msg.Header.Set(k, v)
	}
	if err := c.conn.PublishMsg(msg); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "nats publish", err)
	}
	if err := c.flush(ctx); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "nats flush", err)
	}
	return nil
}

// flush drains the NATS write buffer, honouring the caller's deadline when it
// set one. The SDK's FlushWithContext rejects a deadline-less context, so a
// caller that opted out of deadlines (e.g. context.Background) falls back to the
// plain Flush and its fixed 10s cap. This threads the caller's timeout budget
// through — the Kafka backend already does this — instead of always blocking on
// a fixed 10s round-trip regardless of ctx.
func (c *natsClient) flush(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		return c.conn.FlushWithContext(ctx)
	}
	return c.conn.Flush()
}

func (c *natsClient) Subscribe(ctx context.Context, topic string, h Handler) error {
	cb := func(m *nats.Msg) {
		c.mu.Lock()
		if c.closed {
			// Racing Close: the client is gone, drop the delivery rather than run
			// a handler the teardown can no longer wait for.
			c.mu.Unlock()
			return
		}
		c.hwg.Add(1)
		c.mu.Unlock()
		defer c.hwg.Done()
		msg := Message{Value: m.Data, Headers: map[string]string{}}
		for k, vs := range m.Header {
			if len(vs) == 0 {
				continue
			}
			if k == natsKeyHeader {
				msg.Key = []byte(vs[0])
				continue
			}
			msg.Headers[k] = vs[0]
		}
		_ = h(ctx, msg)
	}
	var sub *nats.Subscription
	var err error
	if c.groupID == "" {
		sub, err = c.conn.Subscribe(topic, cb)
	} else {
		// Load-balance: a NATS queue group is the counterpart of a Kafka consumer
		// group — subscriptions sharing the group split the stream.
		sub, err = c.conn.QueueSubscribe(topic, c.groupID, cb)
	}
	if err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "nats subscribe", err)
	}
	if err := c.flush(ctx); err != nil {
		_ = sub.Unsubscribe()
		return apperr.Wrap(apperr.CatUnavailable, "nats flush", err)
	}
	c.mu.Lock()
	if c.closed {
		// Lost the race with Close: unsubscribe the reader we just created rather
		// than leaking it (Close already swept the map).
		c.mu.Unlock()
		_ = sub.Unsubscribe()
		return apperr.New(apperr.CatUnavailable, "mq: subscriber closed")
	}
	c.subs[sub] = struct{}{}
	c.wg.Add(1)
	c.mu.Unlock()

	// Stop delivery when the caller's context is cancelled OR the client is
	// closed. Selecting on c.done as well means a caller that passed a
	// non-cancellable context (e.g. Background) does not leak this goroutine
	// past Close, matching the Kafka backend whose Close unblocks its read loop.
	go func() {
		defer c.wg.Done()
		select {
		case <-ctx.Done():
		case <-c.done:
		}
		_ = sub.Unsubscribe()
		c.mu.Lock()
		delete(c.subs, sub)
		c.mu.Unlock()
	}()
	return nil
}

func (c *natsClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done) // wake every per-subscription watcher goroutine
	for s := range c.subs {
		_ = s.Unsubscribe()
	}
	c.subs = nil
	c.mu.Unlock()
	return closeWithin(ctx, func() error {
		c.conn.Close()
		c.wg.Wait()  // no watcher goroutine outlives the teardown
		c.hwg.Wait() // nor does an in-flight handler invocation
		return nil
	})
}

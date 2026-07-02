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
	conn   *nats.Conn
	mu     sync.Mutex
	subs   []*nats.Subscription
	closed bool
}

// NewNATS connects to NATS and returns a publisher and subscriber sharing one
// connection.
func NewNATS(cfg config.MQConfig) (Publisher, Subscriber, error) {
	url := cfg.URL
	if url == "" {
		url = nats.DefaultURL
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.CatUnavailable, "nats connect", err)
	}
	c := &natsClient{conn: conn}
	return c, c, nil
}

func (c *natsClient) Publish(_ context.Context, topic string, m Message) error {
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
	if err := c.conn.Flush(); err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "nats flush", err)
	}
	return nil
}

func (c *natsClient) Subscribe(ctx context.Context, topic string, h Handler) error {
	sub, err := c.conn.Subscribe(topic, func(m *nats.Msg) {
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
	})
	if err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "nats subscribe", err)
	}
	if err := c.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return apperr.Wrap(apperr.CatUnavailable, "nats flush", err)
	}
	c.mu.Lock()
	if c.closed {
		// Lost the race with Close: unsubscribe the reader we just created rather
		// than leaking it (Close already swept the slice).
		c.mu.Unlock()
		_ = sub.Unsubscribe()
		return apperr.New(apperr.CatUnavailable, "mq: subscriber closed")
	}
	c.subs = append(c.subs, sub)
	c.mu.Unlock()

	// Stop delivery when the caller's context is cancelled, matching the Kafka
	// backend (which stops its read loop on ctx.Done).
	go func() {
		<-ctx.Done()
		_ = sub.Unsubscribe()
	}()
	return nil
}

func (c *natsClient) Close() error {
	c.mu.Lock()
	c.closed = true
	for _, s := range c.subs {
		_ = s.Unsubscribe()
	}
	c.subs = nil
	c.mu.Unlock()
	c.conn.Close()
	return nil
}

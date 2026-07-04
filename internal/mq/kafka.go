//go:build integration

package mq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/segmentio/kafka-go"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// kafkaPublisher and kafkaSubscriber are compiled only under the integration
// tag, since they require a live broker.
type kafkaClient struct {
	brokers []string
	groupID string
	writer  *kafka.Writer
	mu      sync.Mutex
	readers []*kafka.Reader
	closed  bool
	// wg tracks reader goroutines so Close returns only once they have all
	// exited — no goroutine outlives the client.
	wg sync.WaitGroup
}

// NewKafka builds a Kafka publisher/subscriber.
func NewKafka(cfg config.MQConfig) (Publisher, Subscriber, error) {
	brokers, err := splitBrokers(cfg.URL)
	if err != nil {
		return nil, nil, err
	}
	c := &kafkaClient{
		brokers: brokers,
		groupID: cfg.GroupID,
		writer:  &kafka.Writer{Addr: kafka.TCP(brokers...), Balancer: &kafka.LeastBytes{}},
	}
	return c, c, nil
}

// randomGroupID names the throwaway consumer group backing one fan-out
// subscription. Random rather than sequential so independent processes never
// collide into an accidental shared group.
func randomGroupID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "gortexa-" + hex.EncodeToString(b[:]), nil
}

func (c *kafkaClient) Publish(ctx context.Context, topic string, m Message) error {
	headers := make([]kafka.Header, 0, len(m.Headers))
	for k, v := range m.Headers {
		headers = append(headers, kafka.Header{Key: k, Value: []byte(v)})
	}
	err := c.writer.WriteMessages(ctx, kafka.Message{Topic: topic, Key: m.Key, Value: m.Value, Headers: headers})
	if err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "kafka publish", err)
	}
	return nil
}

func (c *kafkaClient) Subscribe(ctx context.Context, topic string, h Handler) error {
	// LastOffset unconditionally: a group with no committed offsets starts at
	// the tail, aligning with NATS live-start instead of replaying retained
	// history; an established explicit group still resumes from its committed
	// offsets. Note the group join is asynchronous — Subscribe returns before
	// it completes, so messages published in that window are not delivered
	// (see the package doc).
	rc := kafka.ReaderConfig{Brokers: c.brokers, Topic: topic, GroupID: c.groupID, StartOffset: kafka.LastOffset}
	explicit := c.groupID != ""
	if !explicit {
		// Fan-out (the cross-backend default): a fresh throwaway group per
		// subscription so every subscriber sees every message. Its broker-side
		// metadata expires via the offsets.retention window.
		gid, err := randomGroupID()
		if err != nil {
			return apperr.Wrap(apperr.CatInternal, "mq: generate group id", err)
		}
		rc.GroupID = gid
	}
	r := kafka.NewReader(rc)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = r.Close()
		return apperr.New(apperr.CatUnavailable, "mq: subscriber closed")
	}
	c.readers = append(c.readers, r)
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		// Close the reader on ctx cancellation or a terminal read error so
		// consumer-group membership and reader goroutines are released promptly.
		defer c.wg.Done()
		defer func() { _ = r.Close() }()
		for {
			// Fetch, run the handler, then commit — never the reverse: committing
			// before the handler (ReadMessage's behaviour) would mark a message
			// consumed that a crash mid-handler never processed. Handler errors are
			// not retried on either backend (see the package doc), so the offset is
			// committed once the handler has had its chance.
			km, err := r.FetchMessage(ctx)
			if err != nil {
				return
			}
			headers := make(map[string]string, len(km.Headers))
			for _, kh := range km.Headers {
				headers[kh.Key] = string(kh.Value)
			}
			_ = h(ctx, Message{Key: km.Key, Value: km.Value, Headers: headers})
			if explicit {
				// A failed commit is left for a later one to cover (offsets are
				// cumulative); the cost is possible redelivery, not loss. Fan-out
				// groups are throwaway and never commit.
				_ = r.CommitMessages(ctx, km)
			}
		}
	}()
	return nil
}

func (c *kafkaClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	readers := c.readers
	c.readers = nil
	c.mu.Unlock()
	return closeWithin(ctx, func() error {
		// Close readers concurrently: each Close waits out a consumer-group
		// leave/rebalance round, so closing sequentially scales O(n) with the
		// subscription count and can blow the caller's shutdown budget.
		var closers sync.WaitGroup
		for _, r := range readers {
			closers.Add(1)
			go func() {
				defer closers.Done()
				_ = r.Close()
			}()
		}
		closers.Wait()
		err := c.writer.Close()
		c.wg.Wait() // no reader goroutine outlives the teardown
		return err
	})
}

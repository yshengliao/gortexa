//go:build integration

package mq

import (
	"context"
	"sync"

	"github.com/segmentio/kafka-go"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// kafkaPublisher and kafkaSubscriber are compiled only under the integration
// tag, since they require a live broker.
type kafkaClient struct {
	brokers []string
	writer  *kafka.Writer
	mu      sync.Mutex
	readers []*kafka.Reader
}

// NewKafka builds a Kafka publisher/subscriber.
func NewKafka(cfg config.MQConfig) (Publisher, Subscriber, error) {
	if cfg.URL == "" {
		return nil, nil, apperr.New(apperr.CatInvalidArgument, "mq.url (kafka brokers) required")
	}
	brokers := []string{cfg.URL}
	c := &kafkaClient{
		brokers: brokers,
		writer:  &kafka.Writer{Addr: kafka.TCP(brokers...), Balancer: &kafka.LeastBytes{}},
	}
	return c, c, nil
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
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: c.brokers, Topic: topic, GroupID: "gortexa"})
	c.mu.Lock()
	c.readers = append(c.readers, r)
	c.mu.Unlock()
	go func() {
		for {
			km, err := r.ReadMessage(ctx)
			if err != nil {
				return
			}
			headers := make(map[string]string, len(km.Headers))
			for _, kh := range km.Headers {
				headers[kh.Key] = string(kh.Value)
			}
			_ = h(ctx, Message{Key: km.Key, Value: km.Value, Headers: headers})
		}
	}()
	return nil
}

func (c *kafkaClient) Close() error {
	c.mu.Lock()
	for _, r := range c.readers {
		_ = r.Close()
	}
	c.mu.Unlock()
	return c.writer.Close()
}

//go:build integration

package mq

import (
	"testing"

	"github.com/segmentio/kafka-go"

	"github.com/yshengliao/gortexa/internal/config"
)

// TestNewKafkaRequiresAllAcks pins the durability fix: the Writer must wait for
// the full in-sync replica set, not the struct-literal zero value RequireNone
// (fire-and-forget) that would let Publish report success before the broker
// stored the message. NewKafka does no I/O, so this needs no live broker.
func TestNewKafkaRequiresAllAcks(t *testing.T) {
	pub, _, err := NewKafka(config.MQConfig{URL: "127.0.0.1:9092"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := pub.(*kafkaClient)
	if !ok {
		t.Fatalf("NewKafka returned %T, want *kafkaClient", pub)
	}
	if c.writer.RequiredAcks != kafka.RequireAll {
		t.Fatalf("RequiredAcks = %v, want RequireAll (fire-and-forget would drop messages silently)", c.writer.RequiredAcks)
	}
}

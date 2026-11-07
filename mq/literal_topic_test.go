package mq

import (
	"context"
	"testing"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// TestValidateLiteralTopic pins the exported helper consumers use before
// building a topic from request data: a wildcard token or an empty token
// broadens a subscription or publish beyond the intended subject.
func TestValidateLiteralTopic(t *testing.T) {
	for _, ok := range []string{"orders", "orders.created", "tenant-a.events_1"} {
		if err := ValidateLiteralTopic(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "orders.*", "orders.>", ">", "*", "orders..created", ".orders", "orders.", "a b"} {
		err := ValidateLiteralTopic(bad)
		if !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Errorf("%q accepted or wrong category: %v", bad, err)
		}
	}
}

// TestNATSPublishRejectsWildcardTopic pins that the core-NATS driver refuses
// to publish to a wildcard subject before touching the connection. A publish
// to "orders.>" is never what a caller means; it only happens when the topic
// was built from untrusted input, and the broker would accept it.
func TestNATSPublishRejectsWildcardTopic(t *testing.T) {
	c := &natsClient{} // no connection: validation must fail first
	for _, topic := range []string{"orders.>", "orders.*", "orders..x"} {
		err := c.Publish(context.Background(), topic, Message{Value: []byte("x")})
		if !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Fatalf("Publish(%q) = %v, want InvalidArgument", topic, err)
		}
	}
}

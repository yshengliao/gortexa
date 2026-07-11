package mq

import (
	"strings"
	"testing"
)

// TestStreamName pins the topic→stream-name derivation: legal topics pass
// through prefixed; topics containing characters a stream name rejects are
// sanitised and hash-suffixed so distinct topics can never collide.
func TestStreamName(t *testing.T) {
	if got := streamName("events"); got != "gortexa_events" {
		t.Fatalf("plain topic = %q, want gortexa_events", got)
	}
	dotted := streamName("orders.created")
	if strings.ContainsAny(dotted, ">*. /\\\t\r\n") {
		t.Fatalf("sanitised name still contains invalid characters: %q", dotted)
	}
	if !strings.HasPrefix(dotted, "gortexa_orders_created_") {
		t.Fatalf("sanitised name = %q, want gortexa_orders_created_<hash>", dotted)
	}
	if streamName("orders.created") != dotted {
		t.Fatal("streamName is not deterministic")
	}
	if streamName("a.b") == streamName("a_b") {
		t.Fatal("distinct topics collide after sanitisation")
	}
	if got := streamName("a_b"); got != "gortexa_a_b" {
		t.Fatalf("legal topic must pass through unhashed: %q", got)
	}
}

// TestSubjectCovered pins the wildcard matching used to adopt an
// operator-provisioned stream: "*" matches exactly one token, ">" one or more
// remaining tokens.
func TestSubjectCovered(t *testing.T) {
	cases := []struct {
		subjects []string
		topic    string
		want     bool
	}{
		{[]string{"events"}, "events", true},
		{[]string{"other"}, "events", false},
		{[]string{"orders.*"}, "orders.created", true},
		{[]string{"orders.*"}, "orders", false},
		{[]string{"orders.*"}, "orders.created.eu", false},
		{[]string{"orders.>"}, "orders.created.eu", true},
		{[]string{"orders.>"}, "orders", false},
		{[]string{"*.created"}, "orders.created", true},
		{[]string{"a", "b", "events"}, "events", true},
		{nil, "events", false},
	}
	for _, c := range cases {
		if got := subjectCovered(c.subjects, c.topic); got != c.want {
			t.Errorf("subjectCovered(%v, %q) = %v, want %v", c.subjects, c.topic, got, c.want)
		}
	}
}

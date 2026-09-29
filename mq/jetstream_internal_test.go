package mq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/goleak"

	apperr "github.com/yshengliao/gortexa/apperr"
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

// TestValidateJSTopic pins the driver's literal-subject requirement: wildcard
// tokens, whitespace and empty tokens fail loud as InvalidArgument instead of
// surfacing later as a permanent-but-retryable server error.
func TestValidateJSTopic(t *testing.T) {
	for _, topic := range []string{"events", "orders.created", "a1.b-2.c_3"} {
		if err := validateJSTopic(topic); err != nil {
			t.Errorf("validateJSTopic(%q) = %v, want nil", topic, err)
		}
	}
	for _, topic := range []string{"", "orders.*", "*", ">", "orders.>", "foo bar", "a..b", ".a", "a."} {
		err := validateJSTopic(topic)
		if err == nil {
			t.Errorf("validateJSTopic(%q) = nil, want InvalidArgument", topic)
			continue
		}
		if !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Errorf("validateJSTopic(%q) category = %v, want InvalidArgument", topic, err)
		}
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

// fakeJS scripts the JetStream API calls the driver makes, so the stream
// ensure/adopt and publish/subscribe error decisions run without a server.
type fakeJS struct {
	jetstream.JetStream
	lookupErrs []error // successive Stream() results; nil entry = found
	subjects   []string
	createErr  error
	pubErr     error
	consErr    error

	lookups, creates, publishes int
	created                     jetstream.StreamConfig
	consCfg                     jetstream.ConsumerConfig
	handler                     jetstream.MessageHandler
	cc                          *fakeCC
}

func (f *fakeJS) Stream(context.Context, string) (jetstream.Stream, error) {
	f.lookups++
	if len(f.lookupErrs) > 0 {
		err := f.lookupErrs[0]
		f.lookupErrs = f.lookupErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &fakeStream{subjects: f.subjects}, nil
}

func (f *fakeJS) CreateStream(_ context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	f.creates++
	f.created = cfg
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &fakeStream{subjects: cfg.Subjects}, nil
}

func (f *fakeJS) PublishMsg(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	f.publishes++
	if f.pubErr != nil {
		return nil, f.pubErr
	}
	return &jetstream.PubAck{}, nil
}

func (f *fakeJS) CreateOrUpdateConsumer(_ context.Context, _ string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	f.consCfg = cfg
	if f.consErr != nil {
		return nil, f.consErr
	}
	return &fakeConsumer{js: f}, nil
}

type fakeStream struct {
	jetstream.Stream
	subjects []string
}

func (s *fakeStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: jetstream.StreamConfig{Subjects: s.subjects}}
}

type fakeConsumer struct {
	jetstream.Consumer
	js *fakeJS
}

func (c *fakeConsumer) Consume(h jetstream.MessageHandler, _ ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	c.js.handler = h
	c.js.cc = &fakeCC{}
	return c.js.cc, nil
}

type fakeCC struct {
	jetstream.ConsumeContext
	stopped bool
}

func (c *fakeCC) Stop() { c.stopped = true }

func newFakeJSClient(f *fakeJS, groupID string) *jsClient {
	return &jsClient{
		js:        f,
		groupID:   groupID,
		streams:   make(map[string]struct{}),
		consumers: make(map[jetstream.ConsumeContext]struct{}),
		done:      make(chan struct{}),
	}
}

// TestEnsureStream pins the lookup-then-create policy: an absent stream is
// created with bounded retention, an existing one is adopted only when it
// covers the topic, a lost creation race re-looks-up the winner, and a
// successful ensure is cached.
func TestEnsureStream(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name        string
		f           *fakeJS
		wantCat     apperr.Category // "" = success
		wantCreates int
		wantLookups int
	}{
		{"adopts covering stream", &fakeJS{subjects: []string{"orders.*"}}, "", 0, 1},
		{"rejects non-covering stream", &fakeJS{subjects: []string{"other"}}, apperr.CatFailedPrecondition, 0, 1},
		{"creates absent stream", &fakeJS{lookupErrs: []error{jetstream.ErrStreamNotFound}}, "", 1, 1},
		{"create failure is unavailable", &fakeJS{lookupErrs: []error{jetstream.ErrStreamNotFound}, createErr: boom}, apperr.CatUnavailable, 1, 1},
		{"lost create race adopts winner", &fakeJS{
			lookupErrs: []error{jetstream.ErrStreamNotFound, nil},
			createErr:  jetstream.ErrStreamNameAlreadyInUse,
			subjects:   []string{"orders.created"},
		}, "", 1, 2},
		{"lost create race to non-covering winner", &fakeJS{
			lookupErrs: []error{jetstream.ErrStreamNotFound, nil},
			createErr:  jetstream.ErrStreamNameAlreadyInUse,
			subjects:   []string{"orders.deleted"},
		}, apperr.CatFailedPrecondition, 1, 2},
		{"lookup failure is unavailable", &fakeJS{lookupErrs: []error{boom}}, apperr.CatUnavailable, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeJSClient(tc.f, "")
			err := c.ensureStream(context.Background(), "orders.created")
			if tc.wantCat == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !apperr.Is(err, tc.wantCat) {
				t.Fatalf("err = %v, want %v", err, tc.wantCat)
			}
			if tc.f.creates != tc.wantCreates || tc.f.lookups != tc.wantLookups {
				t.Fatalf("creates=%d lookups=%d, want %d/%d", tc.f.creates, tc.f.lookups, tc.wantCreates, tc.wantLookups)
			}
			if tc.wantCreates > 0 && (tc.f.created.MaxAge != jsStreamMaxAge ||
				len(tc.f.created.Subjects) != 1 || tc.f.created.Subjects[0] != "orders.created") {
				t.Fatalf("created stream config = %+v, want bounded single-subject stream", tc.f.created)
			}
			// A second ensure is served from the cache only after a success.
			before := tc.f.lookups
			_ = c.ensureStream(context.Background(), "orders.created")
			if cached := tc.f.lookups == before; cached != (tc.wantCat == "") {
				t.Fatalf("second ensure served from cache = %v, want %v", cached, tc.wantCat == "")
			}
		})
	}
}

// TestJSPublishErrors pins Publish's error decisions: caller mistakes are
// InvalidArgument before any I/O, a broker failure is Unavailable, and only a
// stream-gone failure drops the ensure cache so the next Publish re-creates
// the stream instead of failing forever.
func TestJSPublishErrors(t *testing.T) {
	ctx := context.Background()
	f := &fakeJS{subjects: []string{"orders.created"}}
	c := newFakeJSClient(f, "")
	if err := c.Publish(ctx, "orders.*", Message{}); !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("wildcard topic = %v, want InvalidArgument", err)
	}
	if err := c.Publish(ctx, "orders.created", Message{Headers: map[string]string{"Nats-Msg-Id": "x"}}); !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("reserved header = %v, want InvalidArgument", err)
	}
	if f.lookups != 0 || f.publishes != 0 {
		t.Fatal("invalid input reached the broker")
	}
	if err := c.Publish(ctx, "orders.created", Message{Value: []byte("v")}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		err    error
		forget bool
	}{
		{errors.New("timeout"), false},
		{jetstream.ErrNoStreamResponse, true},
		{fmt.Errorf("publish: %w", jetstream.ErrStreamNotFound), true},
	} {
		f.pubErr = tc.err
		if err := c.Publish(ctx, "orders.created", Message{}); !apperr.Is(err, apperr.CatUnavailable) {
			t.Fatalf("publish error %v = %v, want Unavailable", tc.err, err)
		}
		_, cached := c.streams["orders.created"]
		if cached == tc.forget {
			t.Fatalf("publish error %v: cached=%v, want forget=%v", tc.err, cached, tc.forget)
		}
		c.markEnsured("orders.created")
	}
}

// TestJSSubscribe pins the consumer config (explicit ack, MaxDeliver bound,
// exact filter, durable only for a group), the error mapping, and that the
// watcher stops the consumer when the subscription context ends.
func TestJSSubscribe(t *testing.T) {
	defer goleak.VerifyNone(t)
	noop := func(context.Context, Message) error { return nil }

	t.Run("errors", func(t *testing.T) {
		ctx := context.Background()
		f := &fakeJS{subjects: []string{"orders.created"}}
		c := newFakeJSClient(f, "bad.group")
		if err := c.Subscribe(ctx, "orders.>", noop); !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Fatalf("wildcard = %v, want InvalidArgument", err)
		}
		f.consErr = jetstream.ErrInvalidConsumerName
		if err := c.Subscribe(ctx, "orders.created", noop); !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Fatalf("invalid group = %v, want InvalidArgument", err)
		}
		f.consErr = jetstream.ErrStreamNotFound
		if err := c.Subscribe(ctx, "orders.created", noop); !apperr.Is(err, apperr.CatUnavailable) {
			t.Fatalf("stream gone = %v, want Unavailable", err)
		}
		if _, cached := c.streams["orders.created"]; cached {
			t.Fatal("stream-gone consumer error must drop the ensure cache")
		}
		f.lookupErrs = []error{errors.New("down")}
		if err := c.Subscribe(ctx, "orders.created", noop); !apperr.Is(err, apperr.CatUnavailable) {
			t.Fatalf("ensure failure = %v, want Unavailable", err)
		}
	})

	for _, group := range []string{"", "workers"} {
		t.Run("group="+group, func(t *testing.T) {
			f := &fakeJS{subjects: []string{"orders.created"}}
			c := newFakeJSClient(f, group)
			ctx, cancel := context.WithCancel(context.Background())
			if err := c.Subscribe(ctx, "orders.created", noop); err != nil {
				t.Fatal(err)
			}
			cfg := f.consCfg
			if cfg.AckPolicy != jetstream.AckExplicitPolicy || cfg.MaxDeliver != jsMaxDeliver ||
				cfg.FilterSubject != "orders.created" || cfg.DeliverPolicy != jetstream.DeliverNewPolicy || cfg.Durable != group {
				t.Fatalf("consumer config = %+v", cfg)
			}
			m := &fakeJSMsg{delivered: 1}
			f.handler(m)
			if !m.acked {
				t.Fatal("Consume was not wired to deliver")
			}
			cancel()
			c.wg.Wait()
			if !f.cc.stopped {
				t.Fatal("cancelled subscription did not stop its consumer")
			}
			if len(c.consumers) != 0 {
				t.Fatalf("stopped consumer still tracked: %d", len(c.consumers))
			}
		})
	}

	t.Run("closed client", func(t *testing.T) {
		f := &fakeJS{subjects: []string{"orders.created"}}
		c := newFakeJSClient(f, "")
		c.closed = true
		if err := c.Subscribe(context.Background(), "orders.created", noop); !apperr.Is(err, apperr.CatUnavailable) {
			t.Fatalf("Subscribe after Close = %v, want Unavailable", err)
		}
		if !f.cc.stopped {
			t.Fatal("consumer started after Close was not stopped")
		}
	})
}

// TestStreamGone pins which errors count as the backing stream vanishing —
// only those justify re-running the ensure round-trip.
func TestStreamGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{jetstream.ErrStreamNotFound, true},
		{jetstream.ErrNoStreamResponse, true},
		{fmt.Errorf("wrapped: %w", jetstream.ErrStreamNotFound), true},
		{errors.New("timeout"), false},
		{nil, false},
	} {
		if got := streamGone(tc.err); got != tc.want {
			t.Errorf("streamGone(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

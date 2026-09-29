package mq

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
)

// jsStreamMaxAge bounds the disk footprint of framework-created streams
// (limits retention, 24h age cap). An operator needing different retention
// pre-creates the stream: ensureStream adopts an existing stream after
// verifying it covers the topic, and never mutates its config.
const jsStreamMaxAge = 24 * time.Hour

// jsMaxDeliver bounds how often the server delivers one message, so a poison
// message that fails every attempt is eventually dropped instead of
// redelivering until the stream's MaxAge — forever on an adopted stream with
// no age limit — while holding one of the consumer's ack-pending slots.
const jsMaxDeliver = 10

// jsNakBackoff paces redelivery after a handler error, indexed by the
// delivery count (the last step repeats). A plain Nak would redeliver
// immediately and hot-loop a poison message. It is applied through
// NakWithDelay rather than ConsumerConfig.BackOff, which would also replace
// AckWait with its first step and redeliver any handler slower than 1s.
var jsNakBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

// jsClient implements Publisher and Subscriber on NATS JetStream. Relative to
// the core-NATS client it adds durability: Publish blocks on the server's
// PubAck, and a handler error triggers redelivery (at-least-once), so
// handlers must be idempotent.
type jsClient struct {
	conn    *nats.Conn
	js      jetstream.JetStream
	groupID string

	mu sync.Mutex
	// streams caches topics whose stream is already ensured, bounding the
	// ensure round-trip to once per topic per client.
	streams map[string]struct{}
	// consumers holds the live ConsumeContexts so Close can stop them all.
	consumers map[jetstream.ConsumeContext]struct{}
	closed    bool
	// done is closed once by Close so per-subscription watcher goroutines exit
	// even when the caller passed a non-cancellable context (e.g. Background).
	done chan struct{}
	// wg tracks watcher goroutines so Close returns only once they have all
	// exited — no goroutine outlives the client.
	wg sync.WaitGroup
	// hwg tracks in-flight handler invocations so Close's teardown waits for
	// them — shutdown never abandons a running handler. Add happens under mu
	// against the closed flag, so it can never race a Wait that started at zero.
	hwg sync.WaitGroup
}

// NewJetStream connects to NATS and returns a JetStream-backed publisher and
// subscriber sharing one connection. It fails loud when the server does not
// have JetStream enabled: jetstream.New does no I/O, so an explicit
// AccountInfo probe is the earliest the misconfiguration can surface (the
// probe self-caps at the library's 5s default API timeout).
func NewJetStream(cfg config.MQConfig) (Publisher, Subscriber, error) {
	url := cfg.URL.Reveal()
	if url == "" {
		url = nats.DefaultURL
	} else if err := validateServerList(url); err != nil {
		return nil, nil, err
	}
	conn, err := nats.Connect(url, natsConnectOptions()...)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.CatUnavailable, "jetstream connect", sanitizeConnectErr(err))
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, apperr.Wrap(apperr.CatUnavailable, "jetstream init", err)
	}
	if _, err := js.AccountInfo(context.Background()); err != nil {
		conn.Close()
		if errors.Is(err, jetstream.ErrJetStreamNotEnabled) || errors.Is(err, jetstream.ErrJetStreamNotEnabledForAccount) {
			return nil, nil, apperr.Wrap(apperr.CatFailedPrecondition, "mq: jetstream not enabled on server", err)
		}
		return nil, nil, apperr.Wrap(apperr.CatUnavailable, "jetstream account probe", err)
	}
	c := &jsClient{
		conn:      conn,
		js:        js,
		groupID:   cfg.GroupID,
		streams:   make(map[string]struct{}),
		consumers: make(map[jetstream.ConsumeContext]struct{}),
		done:      make(chan struct{}),
	}
	return c, c, nil
}

// validateJSTopic rejects topics this driver cannot serve. Unlike core NATS,
// every topic needs a concrete stream subject: a wildcard token would make
// the per-topic streams overlap (the server rejects overlapping subjects
// permanently), and whitespace or an empty token is an invalid NATS subject
// the server would reject on every retry. Failing loud here surfaces the
// misuse as InvalidArgument instead of a permanent-but-retryable server
// error.
func validateJSTopic(topic string) error {
	if err := validateTopic(topic); err != nil {
		return err
	}
	for tok := range strings.SplitSeq(topic, ".") {
		switch tok {
		case "*", ">":
			return apperr.New(apperr.CatInvalidArgument, "mq: wildcard topics are not supported by the jetstream driver (core nats only)")
		case "":
			return apperr.New(apperr.CatInvalidArgument, "mq: topic contains an empty token")
		}
	}
	return nil
}

// streamName derives a JetStream stream name from a topic. Subjects may
// contain characters that stream names reject (".", wildcards, spaces, path
// separators); those are replaced and a hash of the original topic appended
// so distinct topics can never collide after sanitisation ("a.b" vs "a_b").
func streamName(topic string) string {
	const invalid = ">*. /\\\t\r\n"
	if !strings.ContainsAny(topic, invalid) {
		return "gortexa_" + topic
	}
	sanitised := strings.Map(func(r rune) rune {
		if strings.ContainsRune(invalid, r) {
			return '_'
		}
		return r
	}, topic)
	return "gortexa_" + sanitised + "_" + strconv.FormatUint(xxhash.Sum64String(topic), 16)
}

// subjectCovered reports whether any of the stream's subjects matches topic,
// honouring NATS wildcards ("*" one token, ">" the remainder).
func subjectCovered(subjects []string, topic string) bool {
	tt := strings.Split(topic, ".")
	for _, s := range subjects {
		if subjectMatches(strings.Split(s, "."), tt) {
			return true
		}
	}
	return false
}

func subjectMatches(pattern, topic []string) bool {
	for i, p := range pattern {
		if p == ">" {
			// ">" matches one or more remaining tokens, never zero.
			return i < len(topic)
		}
		if i >= len(topic) {
			return false
		}
		if p != "*" && p != topic[i] {
			return false
		}
	}
	return len(pattern) == len(topic)
}

// ensureStream makes sure a stream covering topic exists, creating one with
// bounded retention when absent. An existing stream is adopted, never
// updated: CreateOrUpdateStream would clobber an operator's retention
// config, so this is deliberately lookup-then-create.
func (c *jsClient) ensureStream(ctx context.Context, topic string) error {
	c.mu.Lock()
	_, ok := c.streams[topic]
	c.mu.Unlock()
	if ok {
		return nil
	}
	name := streamName(topic)
	stream, err := c.js.Stream(ctx, name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		if _, cerr := c.js.CreateStream(ctx, jetstream.StreamConfig{
			Name:     name,
			Subjects: []string{topic},
			MaxAge:   jsStreamMaxAge,
		}); cerr == nil {
			c.markEnsured(topic)
			return nil
		} else if !errors.Is(cerr, jetstream.ErrStreamNameAlreadyInUse) {
			return apperr.Wrap(apperr.CatUnavailable, "jetstream create stream", cerr)
		}
		// Lost a creation race: re-look-up and verify whoever won covers topic.
		stream, err = c.js.Stream(ctx, name)
	}
	if err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "jetstream lookup stream", err)
	}
	if !subjectCovered(stream.CachedInfo().Config.Subjects, topic) {
		return apperr.New(apperr.CatFailedPrecondition, "mq: stream "+name+" does not cover topic "+topic)
	}
	c.markEnsured(topic)
	return nil
}

func (c *jsClient) markEnsured(topic string) {
	c.mu.Lock()
	c.streams[topic] = struct{}{}
	c.mu.Unlock()
}

// forgetStream drops the ensured mark so the next call re-runs ensureStream's
// lookup-then-create. Without it the cache turns a transient broker event — a
// stream removed out of band, a restart with non-durable storage under a
// transparently reconnecting nats.Conn — into a permanent per-topic outage
// that is still reported to callers as retryable, healed only by a restart.
func (c *jsClient) forgetStream(topic string) {
	c.mu.Lock()
	delete(c.streams, topic)
	c.mu.Unlock()
}

// streamGone reports whether err says the stream backing a topic is absent,
// which is the only case worth re-probing: other transient errors leave the
// stream in place and re-running the ensure round-trip would not help.
func streamGone(err error) bool {
	return errors.Is(err, jetstream.ErrStreamNotFound) || errors.Is(err, jetstream.ErrNoStreamResponse)
}

func (c *jsClient) Publish(ctx context.Context, topic string, m Message) error {
	if err := validateJSTopic(topic); err != nil {
		return err
	}
	if err := checkReservedHeaders(m.Headers); err != nil {
		return err
	}
	if err := c.ensureStream(ctx, topic); err != nil {
		return err
	}
	// The returned PubAck is the durability confirmation — unlike core NATS
	// there is no flush: PublishMsg blocks until the server has stored the
	// message (or ctx expires).
	if _, err := c.js.PublishMsg(ctx, natsWireMsg(topic, m)); err != nil {
		if streamGone(err) {
			c.forgetStream(topic)
		}
		return apperr.Wrap(apperr.CatUnavailable, "jetstream publish", err)
	}
	return nil
}

func (c *jsClient) Subscribe(ctx context.Context, topic string, h Handler) error {
	if err := validateJSTopic(topic); err != nil {
		return err
	}
	if err := c.ensureStream(ctx, topic); err != nil {
		return err
	}
	cfg := jetstream.ConsumerConfig{
		AckPolicy: jetstream.AckExplicitPolicy,
		// DeliverNew applies only at consumer creation: a fresh subscription
		// (or fresh group) starts at the stream tail; an established durable
		// resumes from its ack floor. This matches the package contract that a
		// subscription receives messages published after it subscribed.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		// FilterSubject keeps delivery exact even when an operator-provisioned
		// stream carries more subjects than this topic.
		FilterSubject: topic,
		MaxDeliver:    jsMaxDeliver,
	}
	if c.groupID != "" {
		// Load-balance: group members share one durable consumer; the server
		// delivers each message to exactly one of them.
		cfg.Durable = c.groupID
	}
	cons, err := c.js.CreateOrUpdateConsumer(ctx, streamName(topic), cfg)
	if err != nil {
		if errors.Is(err, jetstream.ErrInvalidConsumerName) {
			return apperr.Wrap(apperr.CatInvalidArgument, "mq: group_id is not a valid consumer name", err)
		}
		if streamGone(err) {
			c.forgetStream(topic)
		}
		return apperr.Wrap(apperr.CatUnavailable, "jetstream consumer", err)
	}
	cc, err := cons.Consume(c.deliver(ctx, h))
	if err != nil {
		return apperr.Wrap(apperr.CatUnavailable, "jetstream consume", err)
	}
	c.mu.Lock()
	if c.closed {
		// Lost the race with Close: stop the consumer we just started rather
		// than leaking it (Close already swept the map).
		c.mu.Unlock()
		cc.Stop()
		return apperr.New(apperr.CatUnavailable, "mq: subscriber closed")
	}
	c.consumers[cc] = struct{}{}
	c.wg.Add(1)
	c.mu.Unlock()

	// Stop delivery when the caller's context is cancelled OR the client is
	// closed. Selecting on c.done as well means a caller that passed a
	// non-cancellable context (e.g. Background) does not leak this goroutine
	// past Close. Stop is idempotent, so racing Close's own Stop is safe.
	go func() {
		defer c.wg.Done()
		select {
		case <-ctx.Done():
		case <-c.done:
		}
		cc.Stop()
		c.mu.Lock()
		delete(c.consumers, cc) // safe no-op after Close nils the map
		c.mu.Unlock()
	}()
	return nil
}

// deliver returns the Consume callback for one subscription: it runs h under
// safeInvoke and settles the delivery with the outcome — Ack on success,
// jsSettleFailed on error.
func (c *jsClient) deliver(ctx context.Context, h Handler) jetstream.MessageHandler {
	return func(msg jetstream.Msg) {
		c.mu.Lock()
		if c.closed {
			// Racing Close: drop the delivery un-acked; the server redelivers
			// it later (at-least-once), so no work is lost.
			c.mu.Unlock()
			return
		}
		c.hwg.Add(1)
		c.mu.Unlock()
		defer c.hwg.Done()
		if err := safeInvoke(ctx, msg.Subject(), h, messageFromWire(msg.Data(), msg.Headers())); err != nil {
			jsSettleFailed(msg, err)
			return
		}
		_ = msg.Ack()
	}
}

// jsSettleFailed settles a delivery whose handler failed. InvalidArgument
// means the message itself is bad and no retry can succeed, so it is
// terminated; anything else is negative-acked with a delay that grows with
// the delivery count, bounded overall by the consumer's jsMaxDeliver.
func jsSettleFailed(msg jetstream.Msg, err error) {
	if apperr.Is(err, apperr.CatInvalidArgument) {
		_ = msg.Term()
		return
	}
	step := 0
	if md, merr := msg.Metadata(); merr == nil && md.NumDelivered > 0 {
		step = int(min(md.NumDelivered-1, uint64(len(jsNakBackoff)-1)))
	}
	_ = msg.NakWithDelay(jsNakBackoff[step])
}

func (c *jsClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done) // wake every per-subscription watcher goroutine
	for cc := range c.consumers {
		cc.Stop()
	}
	c.consumers = nil
	c.mu.Unlock()
	return closeWithin(ctx, func() error {
		c.wg.Wait()  // no watcher goroutine outlives the teardown
		c.hwg.Wait() // nor does an in-flight handler invocation
		// The connection closes last so a finishing handler's Ack can still
		// reach the server; a delivery racing Close is dropped un-acked and
		// simply redelivered later.
		c.conn.Close()
		return nil
	})
}

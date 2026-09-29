package mq

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// captureLog swaps slog.Default for a buffer for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestSafeInvokeLogsFailures pins that a recovered panic and a plain handler
// error both leave a log line: neither driver has a caller to return them to,
// so without it a failed message vanishes (core NATS) or redelivers (JetStream)
// with no trace of why.
func TestSafeInvokeLogsFailures(t *testing.T) {
	t.Run("panic with stack", func(t *testing.T) {
		buf := captureLog(t)
		err := safeInvoke(context.Background(), "orders.created", func(context.Context, Message) error {
			panic("boom")
		}, Message{})
		if !apperr.Is(err, apperr.CatInternal) {
			t.Fatalf("err = %v, want Internal", err)
		}
		out := buf.String()
		for _, want := range []string{`"level":"ERROR"`, `"subject":"orders.created"`, `"panic":"boom"`, `"stack":"goroutine`} {
			if !strings.Contains(out, want) {
				t.Fatalf("log %q missing %s", out, want)
			}
		}
	})
	t.Run("handler error", func(t *testing.T) {
		buf := captureLog(t)
		err := safeInvoke(context.Background(), "orders.created", func(context.Context, Message) error {
			return errors.New("db down")
		}, Message{})
		if err == nil {
			t.Fatal("want the handler error back")
		}
		out := buf.String()
		for _, want := range []string{`"level":"WARN"`, `"subject":"orders.created"`, `db down`} {
			if !strings.Contains(out, want) {
				t.Fatalf("log %q missing %s", out, want)
			}
		}
	})
	t.Run("success is silent", func(t *testing.T) {
		buf := captureLog(t)
		if err := safeInvoke(context.Background(), "s", func(context.Context, Message) error { return nil }, Message{}); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Fatalf("unexpected log: %s", buf)
		}
	})
}

// fakeJSMsg records how a delivery was settled.
type fakeJSMsg struct {
	jetstream.Msg
	data      []byte
	header    nats.Header
	delivered uint64
	mdErr     error
	acked     bool
	termed    bool
	nakDelay  time.Duration
}

func (m *fakeJSMsg) Metadata() (*jetstream.MsgMetadata, error) {
	if m.mdErr != nil {
		return nil, m.mdErr
	}
	return &jetstream.MsgMetadata{NumDelivered: m.delivered}, nil
}
func (m *fakeJSMsg) Subject() string                    { return "orders.created" }
func (m *fakeJSMsg) Data() []byte                       { return m.data }
func (m *fakeJSMsg) Headers() nats.Header               { return m.header }
func (m *fakeJSMsg) Ack() error                         { m.acked = true; return nil }
func (m *fakeJSMsg) Term() error                        { m.termed = true; return nil }
func (m *fakeJSMsg) NakWithDelay(d time.Duration) error { m.nakDelay = d; return nil }

// settled reports the single settlement a delivery received, "" for none.
func (m *fakeJSMsg) settled() string {
	switch {
	case m.acked && !m.termed && m.nakDelay == 0:
		return "ack"
	case m.termed && !m.acked && m.nakDelay == 0:
		return "term"
	case m.nakDelay != 0 && !m.acked && !m.termed:
		return "nak " + m.nakDelay.String()
	case !m.acked && !m.termed && m.nakDelay == 0:
		return ""
	}
	return "multiple"
}

// TestJSSettleFailed pins the poison-message policy: an InvalidArgument error
// is terminal, and every other failure backs off further on each redelivery
// instead of retrying at a fixed 1s pace.
func TestJSSettleFailed(t *testing.T) {
	m := &fakeJSMsg{delivered: 1}
	jsSettleFailed(m, apperr.New(apperr.CatInvalidArgument, "bad payload"))
	if !m.termed || m.nakDelay != 0 {
		t.Fatalf("InvalidArgument: termed=%v nak=%v, want Term only", m.termed, m.nakDelay)
	}

	for _, tc := range []struct {
		delivered uint64
		want      time.Duration
	}{
		{1, jsNakBackoff[0]},
		{2, jsNakBackoff[1]},
		{uint64(len(jsNakBackoff)), jsNakBackoff[len(jsNakBackoff)-1]},
		{jsMaxDeliver, jsNakBackoff[len(jsNakBackoff)-1]},
		// A zero count never indexes before the first step.
		{0, jsNakBackoff[0]},
	} {
		m := &fakeJSMsg{delivered: tc.delivered}
		jsSettleFailed(m, errors.New("transient"))
		if m.termed || m.nakDelay != tc.want {
			t.Fatalf("delivery %d: termed=%v nak=%v, want Nak %v", tc.delivered, m.termed, m.nakDelay, tc.want)
		}
	}
}

// TestJSSettleFailedMetadataError pins the fallback when the delivery count
// is unreadable: the shortest delay, never a plain (immediate) Nak or a Term.
func TestJSSettleFailedMetadataError(t *testing.T) {
	m := &fakeJSMsg{mdErr: errors.New("not a jetstream message")}
	jsSettleFailed(m, errors.New("transient"))
	if got, want := m.settled(), "nak "+jsNakBackoff[0].String(); got != want {
		t.Fatalf("settled = %q, want %q", got, want)
	}
}

// TestJSDeliver pins the handler-outcome → settlement table of the JetStream
// consume callback: success acks, a caller-input error terminates, any other
// failure (a panic included) naks with backoff, and a delivery racing Close is
// left unsettled for the server to redeliver.
func TestJSDeliver(t *testing.T) {
	nak := func(d time.Duration) string { return "nak " + d.String() }
	cases := []struct {
		name      string
		closed    bool
		delivered uint64
		h         Handler
		want      string
	}{
		{"success acks", false, 1, func(context.Context, Message) error { return nil }, "ack"},
		{"invalid argument terms", false, 1, func(context.Context, Message) error {
			return apperr.New(apperr.CatInvalidArgument, "bad payload")
		}, "term"},
		{"wrapped invalid argument terms", false, 3, func(context.Context, Message) error {
			return fmt.Errorf("decode: %w", apperr.New(apperr.CatInvalidArgument, "bad payload"))
		}, "term"},
		{"transient error naks with first step", false, 1, func(context.Context, Message) error {
			return errors.New("db down")
		}, nak(jsNakBackoff[0])},
		{"redelivered error backs off further", false, 2, func(context.Context, Message) error {
			return apperr.New(apperr.CatUnavailable, "db down")
		}, nak(jsNakBackoff[1])},
		{"panic naks, never acks", false, 1, func(context.Context, Message) error {
			panic("boom")
		}, nak(jsNakBackoff[0])},
		{"closed client leaves delivery unsettled", true, 1, func(context.Context, Message) error {
			t.Error("handler ran after Close")
			return nil
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			c := &jsClient{closed: tc.closed}
			m := &fakeJSMsg{delivered: tc.delivered}
			c.deliver(context.Background(), tc.h)(m)
			if got := m.settled(); got != tc.want {
				t.Fatalf("settled = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJSDeliverDecodesWire pins that the handler sees the caller's message,
// not the wire form: the reserved header comes back as Key.
func TestJSDeliverDecodesWire(t *testing.T) {
	wire := natsWireMsg("orders.created", Message{Key: []byte("k1"), Value: []byte("v"), Headers: map[string]string{"trace": "t1"}})
	var got Message
	c := &jsClient{}
	m := &fakeJSMsg{data: wire.Data, header: wire.Header, delivered: 1}
	c.deliver(context.Background(), func(_ context.Context, msg Message) error { got = msg; return nil })(m)
	if string(got.Key) != "k1" || string(got.Value) != "v" || got.Headers["trace"] != "t1" {
		t.Fatalf("handler got %+v", got)
	}
	if _, leaked := got.Headers[reservedKeyHeader]; leaked {
		t.Fatalf("reserved key header leaked into caller headers: %v", got.Headers)
	}
	if !m.acked {
		t.Fatal("successful delivery not acked")
	}
}

// TestNATSConnectOptionHandlersLog pins that the connection lifecycle reaches
// slog: an unexpected disconnect, a fatal close and an async error are
// warnings/errors, while a deliberate Close (nil disconnect error, no
// LastError) stays below warn. The handlers are invoked directly, so no
// server is needed.
func TestNATSConnectOptionHandlersLog(t *testing.T) {
	opts := nats.GetDefaultOptions()
	for _, o := range natsConnectOptions() {
		if err := o(&opts); err != nil {
			t.Fatal(err)
		}
	}
	if opts.MaxReconnect != -1 {
		t.Fatalf("MaxReconnect = %d, want -1", opts.MaxReconnect)
	}
	cases := []struct {
		name string
		fire func()
		want []string // empty = no warn/error line
	}{
		{"unexpected disconnect", func() { opts.DisconnectedErrCB(nil, errors.New("eof")) }, []string{`"level":"WARN"`, "nats disconnected", "eof"}},
		{"deliberate disconnect", func() { opts.DisconnectedErrCB(nil, nil) }, nil},
		{"reconnect", func() { opts.ReconnectedCB(&nats.Conn{}) }, nil},
		{"deliberate close", func() { opts.ClosedCB(&nats.Conn{}) }, nil},
		{"fatal close", func() { opts.ClosedCB(nil) }, []string{`"level":"WARN"`, "nats connection closed"}},
		{"async error", func() {
			opts.AsyncErrorCB(nil, &nats.Subscription{Subject: "orders.created"}, nats.ErrSlowConsumer)
		}, []string{`"level":"ERROR"`, `"subject":"orders.created"`, "slow consumer"}},
		{"async error without subscription", func() { opts.AsyncErrorCB(nil, nil, errors.New("x")) }, []string{`"level":"ERROR"`, `"subject":""`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			tc.fire()
			out := buf.String()
			if len(tc.want) == 0 && (strings.Contains(out, `"level":"WARN"`) || strings.Contains(out, `"level":"ERROR"`)) {
				t.Fatalf("unexpected warn/error: %s", out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("log %q missing %s", out, w)
				}
			}
		})
	}
}

// TestNATSInvalidTopicIsInvalidArgument pins driver parity: a topic that can
// never be a valid subject is a caller error on core NATS too, not the
// retryable Unavailable the SDK's ErrBadSubject was wrapped as. Validation
// runs before any connection use, so no server is needed.
func TestNATSInvalidTopicIsInvalidArgument(t *testing.T) {
	c := &natsClient{}
	for _, topic := range []string{"", "a b", "a\tb"} {
		if err := c.Publish(context.Background(), topic, Message{}); !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Fatalf("Publish(%q) = %v, want InvalidArgument", topic, err)
		}
		if err := c.Subscribe(context.Background(), topic, func(context.Context, Message) error { return nil }); !apperr.Is(err, apperr.CatInvalidArgument) {
			t.Fatalf("Subscribe(%q) = %v, want InvalidArgument", topic, err)
		}
	}
}

package mq

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	delivered uint64
	termed    bool
	nakDelay  time.Duration
}

func (m *fakeJSMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.delivered}, nil
}
func (m *fakeJSMsg) Term() error                        { m.termed = true; return nil }
func (m *fakeJSMsg) NakWithDelay(d time.Duration) error { m.nakDelay = d; return nil }

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
	} {
		m := &fakeJSMsg{delivered: tc.delivered}
		jsSettleFailed(m, errors.New("transient"))
		if m.termed || m.nakDelay != tc.want {
			t.Fatalf("delivery %d: termed=%v nak=%v, want Nak %v", tc.delivered, m.termed, m.nakDelay, tc.want)
		}
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

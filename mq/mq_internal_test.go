package mq

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// TestCloseWithin pins the escape-hatch semantics: a teardown that finishes
// returns its own error (or nil), and one that outlives ctx returns the
// wrapped ctx error immediately instead of parking the caller. synctest fake
// time makes the expiry paths deterministic.
func TestCloseWithin(t *testing.T) {
	t.Run("completes in time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			if err := closeWithin(context.Background(), func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("propagates fn error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			want := errors.New("teardown failed")
			if err := closeWithin(context.Background(), func() error { return want }); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	})

	t.Run("deadline expiry abandons blocked teardown", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			release := make(chan struct{})
			err := closeWithin(ctx, func() error { <-release; return nil })
			if !apperr.Is(err, apperr.CatDeadlineExceeded) {
				t.Fatalf("category = %v, want DeadlineExceeded", err)
			}
			close(release) // let the abandoned teardown goroutine drain
		})
	})

	t.Run("cancellation maps to Canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			release := make(chan struct{})
			err := closeWithin(ctx, func() error { <-release; return nil })
			if !apperr.Is(err, apperr.CatCanceled) {
				t.Fatalf("category = %v, want Canceled", err)
			}
			close(release)
		})
	})
}

func TestValidateServerList(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "single server", url: "nats://127.0.0.1:4222"},
		{name: "multi server with spaces", url: "nats://a:4222, nats://b:4222 ,nats://c:4222"},
		{name: "empty url", url: "", wantErr: true},
		{name: "trailing comma", url: "nats://a:4222,", wantErr: true},
		{name: "blank entry", url: "nats://a:4222, ,nats://b:4222", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateServerList(c.url)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !apperr.Is(err, apperr.CatInvalidArgument) {
					t.Fatalf("category = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// checkReservedHeaders is the single choke point every backend's Publish calls,
// so a caller header colliding with the framework's reserved key name is
// rejected uniformly (fail-loud) instead of silently overwriting Message.Key on
// the wire.
func TestCheckReservedHeaders(t *testing.T) {
	if err := checkReservedHeaders(nil); err != nil {
		t.Fatalf("nil headers = %v, want nil", err)
	}
	if err := checkReservedHeaders(map[string]string{"trace": "abc"}); err != nil {
		t.Fatalf("ordinary header = %v, want nil", err)
	}

	err := checkReservedHeaders(map[string]string{reservedKeyHeader: "x"})
	if err == nil {
		t.Fatalf("reserved header %q must be rejected", reservedKeyHeader)
	}
	if !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Fatalf("reserved header error = %v, want InvalidArgument", err)
	}
}

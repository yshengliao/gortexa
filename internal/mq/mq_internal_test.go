package mq

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	apperr "github.com/yshengliao/gortexa/internal/errors"
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

func TestSplitBrokers(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		want    []string
		wantErr bool
	}{
		{name: "single broker", url: "127.0.0.1:9092", want: []string{"127.0.0.1:9092"}},
		{name: "multi broker with spaces", url: "b1:9092, b2:9092 ,b3:9092", want: []string{"b1:9092", "b2:9092", "b3:9092"}},
		{name: "empty url", url: "", wantErr: true},
		{name: "trailing comma", url: "b1:9092,", wantErr: true},
		{name: "blank entry", url: "b1:9092, ,b2:9092", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := splitBrokers(c.url)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				if !apperr.Is(err, apperr.CatInvalidArgument) {
					t.Fatalf("category = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// checkReservedHeaders is the single choke point both backends' Publish call, so a
// caller header colliding with the framework's reserved key name is rejected
// uniformly (fail-loud) instead of silently overwriting Message.Key on the NATS
// backend while passing through as an ordinary header on Kafka.
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

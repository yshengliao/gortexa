package mq_test

import (
	"strings"
	"testing"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/mq"
)

// TestNewDriverErrors covers the factory's driver-selection failure paths.
func TestNewDriverErrors(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.MQConfig
		wantCat apperr.Category
	}{
		{
			name:    "unsupported driver",
			cfg:     config.MQConfig{Driver: "carrier-pigeon"},
			wantCat: apperr.CatInvalidArgument,
		},
		{
			// The Kafka backend was removed; the old driver value must fail loud
			// rather than silently fall back to NATS.
			name:    "kafka driver removed",
			cfg:     config.MQConfig{Driver: "kafka"},
			wantCat: apperr.CatInvalidArgument,
		},
		{
			name:    "nats unreachable url",
			cfg:     config.MQConfig{Driver: "nats", URL: "nats://127.0.0.1:1"},
			wantCat: apperr.CatUnavailable,
		},
		{
			name:    "empty driver defaults to nats",
			cfg:     config.MQConfig{URL: "nats://127.0.0.1:1"},
			wantCat: apperr.CatUnavailable,
		},
		{
			name:    "nats blank server-list entry fails loud",
			cfg:     config.MQConfig{Driver: "nats", URL: "nats://127.0.0.1:1,,nats://127.0.0.1:2"},
			wantCat: apperr.CatInvalidArgument,
		},
		{
			name:    "jetstream unreachable url",
			cfg:     config.MQConfig{Driver: "jetstream", URL: "nats://127.0.0.1:1"},
			wantCat: apperr.CatUnavailable,
		},
		{
			name:    "jetstream blank server-list entry fails loud",
			cfg:     config.MQConfig{Driver: "jetstream", URL: "nats://127.0.0.1:1,,nats://127.0.0.1:2"},
			wantCat: apperr.CatInvalidArgument,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub, sub, err := mq.New(c.cfg)
			if err == nil {
				t.Fatal("New should fail")
			}
			if pub != nil || sub != nil {
				t.Errorf("pub/sub should be nil on error, got %v, %v", pub, sub)
			}
			if !apperr.Is(err, c.wantCat) {
				t.Errorf("category = %v, want %v", err, c.wantCat)
			}
		})
	}
}

// TestNewRedactsCredentialBearingURL pins the connect-error redaction: a URL
// parse failure would otherwise embed the raw URL — credentials included — in
// the error, defeating the Secret typing of mq.url. The "%zz" makes url.Parse
// fail without any server round-trip, on both drivers.
func TestNewRedactsCredentialBearingURL(t *testing.T) {
	for _, driver := range []string{"nats", "jetstream"} {
		t.Run(driver, func(t *testing.T) {
			_, _, err := mq.New(config.MQConfig{Driver: driver, URL: "nats://svc:hunter2%zz@127.0.0.1:4222"})
			if err == nil {
				t.Fatal("New should fail on an unparsable URL")
			}
			if got := err.Error(); strings.Contains(got, "hunter2") {
				t.Fatalf("connect error leaks the credential: %q", got)
			}
		})
	}
}

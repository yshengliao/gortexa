package mq_test

import (
	"testing"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/mq"
)

// TestNewDriverErrors covers driver-selection paths that behave identically in
// both the default and integration builds. The kafka-stub case lives in a
// //go:build !integration file (mq_stub_test.go) because under -tags integration
// NewKafka is the real broker client and does not fail on a valid config.
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
			name:    "nats unreachable url",
			cfg:     config.MQConfig{Driver: "nats", URL: "nats://127.0.0.1:1"},
			wantCat: apperr.CatUnavailable,
		},
		{
			name:    "empty driver defaults to nats",
			cfg:     config.MQConfig{URL: "nats://127.0.0.1:1"},
			wantCat: apperr.CatUnavailable,
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

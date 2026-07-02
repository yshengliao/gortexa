//go:build !integration

package mq_test

import (
	"testing"

	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/mq"
)

// TestKafkaStubUnavailable asserts the default build's kafka stub. Under
// -tags integration NewKafka is the real client, so this expectation only holds
// in the non-integration build (hence the build tag).
func TestKafkaStubUnavailable(t *testing.T) {
	pub, sub, err := mq.New(config.MQConfig{Driver: "kafka", URL: "127.0.0.1:9092"})
	if err == nil {
		t.Fatal("kafka without the integration build must fail")
	}
	if pub != nil || sub != nil {
		t.Errorf("pub/sub should be nil, got %v %v", pub, sub)
	}
	if !apperr.Is(err, apperr.CatInvalidArgument) {
		t.Errorf("category = %v, want InvalidArgument", err)
	}
}

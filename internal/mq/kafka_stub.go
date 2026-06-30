//go:build !integration

package mq

import (
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// NewKafka is available only in integration builds.
func NewKafka(config.MQConfig) (Publisher, Subscriber, error) {
	return nil, nil, apperr.New(apperr.CatInvalidArgument, "mq: kafka requires integration build")
}

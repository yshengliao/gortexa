//go:build integration

package mq

import (
	"context"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	"github.com/yshengliao/gortexa/config"
)

// TestConnectOptionsApplied pins the connect options both NATS-family drivers
// must carry. The SDK defaults (MaxReconnect=60, ReconnectWait=2s) close the
// connection permanently after ~120s of broker unavailability, and with no
// callbacks set that death is silent — so a driver that connects with bare
// nats.Connect(url) is unusable after an outage longer than a rolling upgrade
// and says nothing about it. Asserting on the live conn's Opts covers both
// call sites, not just the option builder.
func TestConnectOptionsApplied(t *testing.T) {
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	url := config.Secret(srv.ClientURL())

	natsPub, _, err := NewNATS(config.MQConfig{URL: url})
	if err != nil {
		t.Fatalf("NewNATS: %v", err)
	}
	t.Cleanup(func() { _ = natsPub.Close(context.Background()) })

	jsPub, _, err := NewJetStream(config.MQConfig{URL: url})
	if err != nil {
		t.Fatalf("NewJetStream: %v", err)
	}
	t.Cleanup(func() { _ = jsPub.Close(context.Background()) })

	for _, tc := range []struct {
		driver string
		conn   *nats.Conn
	}{
		{"nats", natsPub.(*natsClient).conn},
		{"jetstream", jsPub.(*jsClient).conn},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			o := tc.conn.Opts
			if o.MaxReconnect != -1 {
				t.Errorf("MaxReconnect = %d, want -1 (retry forever; the SDK default of 60 closes the connection permanently after ~120s of outage)", o.MaxReconnect)
			}
			if o.ClosedCB == nil {
				t.Error("ClosedCB not set: a terminal close would be invisible")
			}
			if o.DisconnectedErrCB == nil {
				t.Error("DisconnectedErrCB not set: a broker outage would be invisible")
			}
		})
	}
}

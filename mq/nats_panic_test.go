//go:build integration

package mq_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"

	"github.com/yshengliao/gortexa/config"
	"github.com/yshengliao/gortexa/mq"
)

// TestSubscribeHandlerPanicRecovery verifies that a panic raised inside a
// Subscribe handler does not take down the whole process.
//
// A panic on a goroutine that nobody recovers is fatal to the entire Go
// process (not just the goroutine), so this has to run the actual scenario
// in a subprocess: if the delivery callback invokes the handler with no
// recovery, the subprocess dies with an unrecovered-panic exit status and
// "SURVIVED" is never printed. With the handler run under safeInvoke the
// subprocess keeps running, processes the next message on the same
// subscription, and prints "SURVIVED" before exiting 0.
func TestSubscribeHandlerPanicRecovery(t *testing.T) {
	if os.Getenv("GORTEXA_MQ_PANIC_SUBPROCESS") == "1" {
		runPanicSubprocess()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSubscribeHandlerPanicRecovery$", "-test.v=true")
	cmd.Env = append(os.Environ(), "GORTEXA_MQ_PANIC_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess did not exit cleanly (handler panic was not recovered, "+
			"crashing the whole delivery process instead of being confined to the failed "+
			"message): %v\n--- subprocess output ---\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Fatalf("subprocess exited 0 but never reached SURVIVED marker "+
			"(second message was never delivered/processed):\n%s", out)
	}
}

// runPanicSubprocess is the body that actually runs inside the re-exec'd
// subprocess. It subscribes a handler that panics (index out of range) on
// the first delivered message — the malformed-payload scenario — then
// publishes a second, well-formed message on the same subject/subscription.
// If the process survives the first panic, the second message is processed
// and "SURVIVED" is printed before a clean os.Exit(0).
func runPanicSubprocess() {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	pub, sub, err := mq.NewNATS(config.MQConfig{URL: config.Secret(srv.ClientURL())})
	if err != nil {
		fmt.Println("SETUP_FAILED:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	var calls atomic.Int32
	survived := make(chan struct{})

	err = sub.Subscribe(ctx, "orders", func(_ context.Context, m mq.Message) error {
		if calls.Add(1) == 1 {
			var v struct {
				Items []string `json:"items"`
			}
			_ = json.Unmarshal(m.Value, &v)
			_ = v.Items[0] // panics: index out of range on the empty-items payload
			return nil
		}
		close(survived)
		return nil
	})
	if err != nil {
		fmt.Println("SUBSCRIBE_FAILED:", err)
		os.Exit(1)
	}

	// Malformed payload: empty items array triggers the panic above.
	if err := pub.Publish(ctx, "orders", mq.Message{Value: []byte(`{"items":[]}`)}); err != nil {
		fmt.Println("PUBLISH1_FAILED:", err)
		os.Exit(1)
	}
	// Well-formed follow-up: only reached if the subscription is still alive.
	if err := pub.Publish(ctx, "orders", mq.Message{Value: []byte(`{"items":["x"]}`)}); err != nil {
		fmt.Println("PUBLISH2_FAILED:", err)
		os.Exit(1)
	}

	select {
	case <-survived:
		fmt.Println("SURVIVED")
		os.Exit(0)
	case <-time.After(5 * time.Second):
		fmt.Println("TIMEOUT: second message never processed")
		os.Exit(1)
	}
}

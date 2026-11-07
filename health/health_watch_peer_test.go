package health_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"testing/synctest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/yshengliao/gortexa/health"
)

// maxWatchersPerPeer mirrors health.maxWatchersPerPeer.
const maxWatchersPerPeer = 64

func peerCtx(parent context.Context, ip string, port int) context.Context {
	return peer.NewContext(parent, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: port}})
}

// TestGRPCHealthWatchCapsPerPeer pins that the Watch budget is also keyed by
// client address. Watch is unauthenticated and exempt from load shedding, so
// with only the global cap one source could fill all 1024 slots and sit on
// them; every later Watch from anyone then failed with ResourceExhausted,
// which a gRPC client using client-side health checking reads as the backend
// being down. A hoarder must hit its own ceiling while another client still
// gets a stream; reconnecting from another port must not mint a fresh budget;
// and the per-peer count must drain with the streams.
func TestGRPCHealthWatchCapsPerPeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := health.NewRegistry()
		r.Register("svc", static(health.Healthy))
		srv := r.GRPCHealthServer()

		hoarder, cancelHoarder := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for i := range maxWatchersPerPeer {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// A different ephemeral port per stream: the key is the host.
				if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: peerCtx(hoarder, "203.0.113.7", 40000+i)}); err != nil {
					t.Errorf("Watch = %v, want nil", err)
				}
			}()
		}
		synctest.Wait()

		over := &lockedWatch{ctx: peerCtx(hoarder, "203.0.113.7", 50000)}
		overErr := make(chan error, 1)
		go func() { overErr <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, over) }()
		synctest.Wait()
		select {
		case err := <-overErr:
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("Watch past the per-peer cap = %v, want ResourceExhausted", err)
			}
		default:
			t.Fatal("Watch past the per-peer cap was accepted, want ResourceExhausted")
		}

		// Another client is unaffected by the hoarder.
		other, cancelOther := context.WithCancel(context.Background())
		otherErr := make(chan error, 1)
		go func() {
			otherErr <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: peerCtx(other, "198.51.100.9", 1234)})
		}()
		synctest.Wait()
		select {
		case err := <-otherErr:
			t.Fatalf("Watch from a second client returned early with %v, want a live stream", err)
		default:
		}
		cancelOther()
		if err := <-otherErr; err != nil {
			t.Fatalf("second client's Watch = %v, want nil", err)
		}

		// Draining the hoarder's streams frees its budget.
		cancelHoarder()
		wg.Wait()
		again, cancelAgain := context.WithCancel(context.Background())
		againErr := make(chan error, 1)
		go func() {
			againErr <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: peerCtx(again, "203.0.113.7", 60000)})
		}()
		synctest.Wait()
		cancelAgain()
		if err := <-againErr; err != nil {
			t.Fatalf("Watch after the hoarder drained = %v, want nil", err)
		}
	})
}

// TestGRPCHealthWatchPeerKeyAggregatesIPv6 pins the /64 aggregation: a host
// handed a whole /64 must not get 2^64 budgets.
func TestGRPCHealthWatchPeerKeyAggregatesIPv6(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := health.NewRegistry()
		r.Register("svc", static(health.Healthy))
		srv := r.GRPCHealthServer()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		for i := range maxWatchersPerPeer {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Same /64, a different interface id per stream.
				ip := net.ParseIP("2001:db8:1:2::1")
				ip[15] = byte(i + 1)
				ip[14] = byte(i >> 8)
				if err := srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: peerCtx(ctx, ip.String(), 4000)}); err != nil {
					t.Errorf("Watch = %v, want nil", err)
				}
			}()
		}
		synctest.Wait()
		overErr := make(chan error, 1)
		go func() {
			overErr <- srv.Watch(&grpc_health_v1.HealthCheckRequest{}, &lockedWatch{ctx: peerCtx(ctx, "2001:db8:1:2:ffff::1", 4000)})
		}()
		synctest.Wait()
		select {
		case err := <-overErr:
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("Watch from the same /64 past the cap = %v, want ResourceExhausted", err)
			}
		default:
			t.Fatal("a /64 minted more than one per-peer budget")
		}
		cancel()
		wg.Wait()
	})
}

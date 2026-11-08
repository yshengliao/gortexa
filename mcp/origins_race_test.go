package mcp_test

import (
	"net/http"
	"sync"
	"testing"
)

// TestSetAllowedOriginsIsSafeWhileServing pins that the allowlist can be
// swapped under load. SetAllowedOrigins replaced a plain map that every
// request read without a lock, so a consumer reloading CORS origins after
// Run raced the handler (a data race under -race) and, between the make and
// the fills, briefly rejected every browser origin.
func TestSetAllowedOriginsIsSafeWhileServing(t *testing.T) {
	b, ts := newBridgeAndServer(t)
	b.SetAllowedOrigins([]string{"https://app.example"})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				b.SetAllowedOrigins([]string{"https://app.example", "https://other.example"})
			}
		}
	})
	for range 200 {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, nil)
		req.Header.Set("Origin", "https://app.example")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Fatal("an allowed origin was rejected while the allowlist was being replaced")
		}
	}
	close(stop)
	wg.Wait()
}

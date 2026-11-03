package kernel

import (
	"context"
	"net"
)

// listenTCP disables MPTCP even when GODEBUG enables it. This mitigates the
// demonstrated CVE-2026-98432 exploit; hosts still need a patched Linux kernel.
func listenTCP(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	return lc.Listen(ctx, "tcp", addr)
}

//go:build linux

package kernel

import (
	"context"
	"net"
	"syscall"
	"testing"
)

func TestListenTCPDisablesMPTCP(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=1")
	lis, err := listenTCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	raw, err := lis.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var protocol int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		protocol, socketErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
	}); err != nil {
		t.Fatal(err)
	}
	if socketErr != nil {
		t.Fatal(socketErr)
	}
	if protocol != syscall.IPPROTO_TCP {
		t.Fatalf("listener protocol = %d, want TCP (%d)", protocol, syscall.IPPROTO_TCP)
	}
}

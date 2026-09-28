package resp

import (
	"bufio"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

// TestReadReplyBulkLengthDoesNotPreallocate pins that a bulk length header is
// treated as a claim, not an allocation size: a peer that announces the
// maximum 512 MiB and then delivers a few bytes must cost memory proportional
// to what arrived. Allocating the claimed length up front let one 12-byte
// header per pooled connection pin gigabytes until the read deadline.
func TestReadReplyBulkLengthDoesNotPreallocate(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("$536870912\r\nabc"))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := readReply(r)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("readReply allocated %d bytes for a 3-byte payload", got)
	}
}

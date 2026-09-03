package resp

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// infiniteSimpleString is an io.Reader that endlessly streams 'A' bytes
// after an initial "+" — simulating a server (or MITM) sending a
// simple-string reply that never terminates with CRLF.
type infiniteSimpleString struct {
	sentPrefix bool
}

func (r *infiniteSimpleString) Read(p []byte) (int, error) {
	if !r.sentPrefix {
		r.sentPrefix = true
		n := copy(p, "+")
		return n, nil
	}
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// TestReadLineBoundsLineLength verifies that readLine (and therefore
// readReply, which calls it for every reply type) refuses to accumulate an
// unbounded amount of memory for a single CRLF-terminated line. A malicious
// or compromised server that sends a simple-string/error/integer header
// line and never emits CRLF must be rejected once the accumulated line
// exceeds maxLineLen, instead of being read forever.
//
// The fake transport is capped at 64 MiB via a LimitReader — 1024x
// maxLineLen, and far below what an attacker could send. An unbounded
// readLine keeps asking the LimitReader for more, exhausts it, and only
// then returns an EOF-derived error, having buffered the whole 64 MiB into
// `full`; a bounded one rejects the line on its own terms long before that.
func TestReadLineBoundsLineLength(t *testing.T) {
	const capBytes = 64 << 20 // 64 MiB ceiling for this test's fake network

	limited := io.LimitReader(&infiniteSimpleString{}, capBytes)
	r := bufio.NewReaderSize(limited, 4096)

	_, err := readReply(r)
	if err == nil {
		t.Fatal("readReply returned no error for a non-terminating reply line")
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		t.Fatalf("readLine had no length bound: it only stopped because the fake transport was exhausted after buffering %d bytes into memory (err=%v)", capBytes, err)
	}
	if !strings.Contains(err.Error(), "line too long") {
		t.Fatalf("got %v, want a line-length-bound error", err)
	}
}

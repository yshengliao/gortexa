// Package resp is a minimal, dependency-free RESP2 Redis client — just the
// GET/SET/DEL/PING surface the cache needs, plus a bounded connection pool.
// It replaces a third-party client so the framework pulls no external Redis
// dependency; the wire format follows https://redis.io/docs/reference/protocol-spec.
package resp

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
)

const (
	// maxBulkLen guards against a malicious or corrupt length header triggering a
	// huge allocation; it is Redis's proto-max-bulk-len default (512 MiB).
	maxBulkLen = 512 << 20
	// maxLineLen bounds a single CRLF-terminated line. Legitimate RESP2 status,
	// error and integer lines are a few dozen bytes, and bulk payloads take the
	// length-prefixed path guarded by maxBulkLen instead of growing a line — so
	// nothing well-formed comes close. Without it a peer that opens a reply and
	// never sends CRLF grows the accumulation buffer at link rate for as long as
	// the read deadline allows, or forever when ReadTimeout is disabled.
	maxLineLen  = 64 << 10
	nullBulkLen = -1
)

// Error is an error reply (-ERR ...) from the server.
type Error string

func (e Error) Error() string { return string(e) }

// writeCommand serialises args as a RESP2 array of bulk strings. Supported arg
// types are string, []byte, int and int64 — anything else is a programming
// error and returns an error rather than sending Go-formatted garbage.
func writeCommand(w *bufio.Writer, args ...any) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		var s string
		switch v := arg.(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		case int:
			s = strconv.Itoa(v)
		case int64:
			s = strconv.FormatInt(v, 10)
		default:
			return fmt.Errorf("resp: unsupported argument type %T", arg)
		}
		if _, err := fmt.Fprintf(w, "$%d\r\n", len(s)); err != nil {
			return err
		}
		if _, err := w.WriteString(s); err != nil {
			return err
		}
		if _, err := w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// readReply reads one RESP2 reply. Returns: string (simple/bulk), int64,
// Error (server error), nil (null bulk/array), or []any (array).
func readReply(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("resp: empty reply line")
	}
	switch line[0] {
	case '+':
		return string(line[1:]), nil
	case '-':
		return Error(line[1:]), nil
	case ':':
		return parseInt(line[1:])
	case '$':
		n, err := parseInt(line[1:])
		if err != nil {
			return nil, err
		}
		if n == nullBulkLen {
			return nil, nil
		}
		if n < 0 || n > maxBulkLen {
			return nil, fmt.Errorf("resp: bulk length out of range: %d", n)
		}
		buf := make([]byte, n+2) // payload + trailing CRLF
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		// Validate the framing terminator: the same adversarial-input posture as
		// the length bound above. A missing CRLF means a length/payload desync, so
		// fail loudly here instead of silently returning bytes that belong to the
		// next reply and corrupting every command after it on this connection.
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return nil, fmt.Errorf("resp: bulk string not CRLF-terminated")
		}
		return string(buf[:n]), nil
	default:
		// This client only issues GET/SET/DEL/PING/AUTH/SELECT, whose replies are
		// simple strings, bulk strings, integers or errors — never arrays ('*').
		// Not decoding arrays keeps the codec minimal and gives a malicious or
		// misbehaving server no unbounded-recursion / large-allocation surface.
		return nil, fmt.Errorf("resp: unexpected reply type %q", line[0])
	}
}

// readLine reads one CRLF-terminated line, stripping the CRLF.
func readLine(r *bufio.Reader) ([]byte, error) {
	line, isPrefix, err := r.ReadLine()
	if err != nil {
		return nil, err
	}
	if !isPrefix {
		return line, nil
	}
	// Line longer than bufio's buffer: accumulate a copy, bounded by maxLineLen
	// so a never-terminated line cannot be grown without limit. The resulting
	// error is not a resp.Error, so the caller discards the connection.
	full := append([]byte(nil), line...)
	for isPrefix && err == nil {
		line, isPrefix, err = r.ReadLine()
		full = append(full, line...)
		if len(full) > maxLineLen {
			return nil, fmt.Errorf("resp: reply line too long")
		}
	}
	return full, err
}

func parseInt(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("resp: empty integer")
	}
	var n, sign int64 = 0, 1
	start := 0
	switch b[0] {
	case '-':
		sign, start = -1, 1
	case '+':
		start = 1
	}
	if start == len(b) {
		return 0, fmt.Errorf("resp: no digits after sign")
	}
	for i := start; i < len(b); i++ {
		if b[i] < '0' || b[i] > '9' {
			return 0, fmt.Errorf("resp: invalid integer %q", b)
		}
		d := int64(b[i] - '0')
		if n > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("resp: integer overflow")
		}
		n = n*10 + d
	}
	return n * sign, nil
}

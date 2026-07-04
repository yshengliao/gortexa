package resp

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestWriteCommand(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeCommand(bw, "SET", "k", "v", "PX", int64(1000)); err != nil {
		t.Fatal(err)
	}
	_ = bw.Flush()
	want := "*5\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nPX\r\n$4\r\n1000\r\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func TestWriteCommandRejectsUnsupportedType(t *testing.T) {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	if err := writeCommand(bw, "SET", 3.14); err == nil {
		t.Fatal("want error for unsupported arg type")
	}
}

func TestReadReply(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want any
	}{
		{"simple string", "+OK\r\n", "OK"},
		{"integer", ":42\r\n", int64(42)},
		{"negative integer", ":-5\r\n", int64(-5)},
		{"bulk string", "$5\r\nhello\r\n", "hello"},
		{"empty bulk string", "$0\r\n\r\n", ""},
		{"null bulk (miss)", "$-1\r\n", nil},
		{"null array", "*-1\r\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readReply(bufio.NewReader(strings.NewReader(c.wire)))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestReadReplyArray(t *testing.T) {
	got, err := readReply(bufio.NewReader(strings.NewReader("*2\r\n$3\r\nfoo\r\n:7\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := got.([]any)
	if !ok || len(arr) != 2 || arr[0] != "foo" || arr[1] != int64(7) {
		t.Fatalf("got %#v", got)
	}
}

func TestReadReplyError(t *testing.T) {
	got, err := readReply(bufio.NewReader(strings.NewReader("-WRONGTYPE bad\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := got.(Error); !ok || e.Error() != "WRONGTYPE bad" {
		t.Fatalf("got %#v", got)
	}
}

func TestReadReplyRejectsOversizedBulk(t *testing.T) {
	// A length header beyond maxBulkLen must be rejected, not allocated.
	if _, err := readReply(bufio.NewReader(strings.NewReader("$999999999999\r\n"))); err == nil {
		t.Fatal("want out-of-range error")
	}
}

func TestReadReplyRejectsMisframedBulk(t *testing.T) {
	// Declared length 5 but the payload+terminator isn't CRLF-terminated: a
	// framing desync must fail loudly, not return corrupt bytes.
	if _, err := readReply(bufio.NewReader(strings.NewReader("$5\r\nhelloXX"))); err == nil {
		t.Fatal("want CRLF-termination error")
	}
}

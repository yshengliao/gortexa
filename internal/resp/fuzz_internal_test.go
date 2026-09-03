package resp

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func FuzzReadReply(f *testing.F) {
	seeds := []string{
		"+OK\r\n",
		"-ERR bad\r\n",
		":123\r\n",
		"$5\r\nhello\r\n",
		"$-1\r\n",
		"$0\r\n\r\n",
		"$536870913\r\n",
		":9223372036854775808\r\n",
		"$\r\n",
		"*2\r\n$1\r\na\r\n$1\r\nb\r\n",
		"$3\r\nab\r\n",
		":\r\n",
		"-\r\n",
		"+" + strings.Repeat("A", 70000) + "\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := readReply(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return
		}
		switch x := v.(type) {
		case string:
			if len(x) > maxBulkLen {
				t.Fatalf("string result exceeds maxBulkLen: %d", len(x))
			}
		case nil, int64, Error:
			// fine
		default:
			t.Fatalf("unexpected result type %T", v)
		}
	})
}

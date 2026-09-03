package interceptor_test

import (
	"testing"

	"github.com/yshengliao/gortexa/interceptor"
)

func FuzzValidRequestID(f *testing.F) {
	seeds := []string{"", "abc-123_ID.45", "a", "..--__", "req id", "日本語", "a\x00b"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !interceptor.ValidRequestID(s) {
			return
		}
		if len(s) > 128 {
			t.Fatalf("ValidRequestID accepted a string longer than 128 bytes: %d", len(s))
		}
		for _, c := range s {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '-', c == '_', c == '.':
			default:
				t.Fatalf("ValidRequestID accepted disallowed rune %q in %q", c, s)
			}
		}
	})
}

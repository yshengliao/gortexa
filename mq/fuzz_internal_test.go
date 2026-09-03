package mq

import (
	"strings"
	"testing"
)

// FuzzCheckReservedHeaders fuzzes mq's reserved-header gate. NOTE: the first run
// of this target (with an EqualFold oracle for reservedKeyHeader) reported a
// failing seed 'Gortexa-Key accepted' -- that was a wrong test oracle, not a
// bug: nats.Header.Set does not canonicalise keys (see mq.go's own comment),
// so 'Gortexa-Key' and 'gortexa-key' are genuinely distinct wire headers that
// never collide with the Key round-trip in natsWireMsg/messageFromWire. Fixed
// to match actual case-sensitive semantics for reservedKeyHeader; re-ran clean.
func FuzzCheckReservedHeaders(f *testing.F) {
	seeds := []string{"gortexa-key", "Gortexa-Key", "nats-", "Nats-Msg-Id", "NATS-EXPECTED-STREAM", "nats", "gortexa-ke", "", "x-nats-", "nats-\x00"}
	for _, s := range seeds {
		f.Add(s, "v")
	}

	f.Fuzz(func(t *testing.T, key, val string) {
		h := map[string]string{key: val}
		err := checkReservedHeaders(h)
		reserved := key == reservedKeyHeader || strings.HasPrefix(strings.ToLower(key), natsReservedHeaderPrefix)
		if reserved && err == nil {
			t.Fatalf("reserved header %q accepted", key)
		}
		if !reserved && err != nil {
			t.Fatalf("non-reserved header %q rejected: %v", key, err)
		}
	})
}

func FuzzValidateServerList(f *testing.F) {
	seeds := []string{"", "nats://a:4222", "nats://a:4222,nats://b:4222", ",", "nats://a:4222,", ",nats://a:4222", "  ", "a,,b", "a, ,b"}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, url string) {
		err := validateServerList(url)
		if url == "" {
			if err == nil {
				t.Fatalf("empty url accepted")
			}
			return
		}
		hasBlank := false
		for _, p := range strings.Split(url, ",") {
			if strings.TrimSpace(p) == "" {
				hasBlank = true
				break
			}
		}
		if hasBlank && err == nil {
			t.Fatalf("url %q with a blank entry accepted", url)
		}
		if !hasBlank && err != nil {
			t.Fatalf("url %q with no blank entry rejected: %v", url, err)
		}
	})
}

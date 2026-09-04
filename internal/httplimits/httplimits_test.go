package httplimits_test

import (
	"testing"

	"github.com/yshengliao/gortexa/httpcompat"
	"github.com/yshengliao/gortexa/internal/httplimits"
)

// The two inbound HTTP surfaces sit on the same port and face the same clients,
// so their body cap has to be one number. It used to be declared twice — once
// exported in httpcompat, once unexported in mcp, with only a comment claiming
// they matched and nothing checking it. The mcp copy being unexported meant
// httpcompat could not have referenced it even deliberately.
func TestGatewayCapTracksTheSharedLimit(t *testing.T) {
	if httpcompat.MaxRequestBytes != httplimits.MaxRequestBytes {
		t.Fatalf("httpcompat.MaxRequestBytes = %d, shared limit = %d: the two HTTP "+
			"surfaces must cap request bodies identically",
			httpcompat.MaxRequestBytes, httplimits.MaxRequestBytes)
	}
}

// The predicate gates what reaches outgoing gRPC metadata on both surfaces: a
// byte outside this range makes the loopback client fail with codes.Internal
// before the chain runs, so the caller gets an opaque 500 rather than the
// chain's real verdict.
func TestPrintableASCIIMatchesTheMetadataRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"bearer token", "Bearer abc.def.ghi", true},
		{"space is the low bound", " ", true},
		{"tilde is the high bound", "~", true},
		{"tab is below the range", "a\tb", false},
		{"newline is below the range", "a\nb", false},
		{"NUL is below the range", "a\x00b", false},
		{"DEL is above the range", "a\x7fb", false},
		{"non-ASCII is above the range", "aéb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := httplimits.PrintableASCII(tc.in); got != tc.want {
				t.Errorf("PrintableASCII(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

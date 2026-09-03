package httpcompat

import (
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/auth"
	"github.com/yshengliao/gortexa/interceptor"
)

func FuzzHeaderMatchers(f *testing.F) {
	seeds := []string{
		"Authorization",
		"authorization",
		"Grpc-Metadata-Authorization",
		"Grpc-Metadata-authorization",
		"grpc-metadata-x-gortexa-peer-ip",
		"X-Request-Id",
		"Grpc-Metadata-X-Request-Id",
		"X-Tenant",
		"\x00Authorization",
		"Authorization\r\n",
		"GRPC-METADATA-AUTHORIZATION",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	canonical := func(s string) bool {
		return strings.EqualFold(s, "Authorization") || strings.EqualFold(s, "X-Request-Id")
	}

	f.Fuzz(func(t *testing.T, data string) {
		if k, ok := incomingHeaderMatcher(data); ok {
			if strings.EqualFold(k, auth.MetadataKey) ||
				strings.EqualFold(k, interceptor.RequestIDMetadataKey) ||
				strings.EqualFold(k, interceptor.PeerIPMetaKey) {
				if !canonical(data) {
					t.Fatalf("incomingHeaderMatcher(%q) mapped to trusted key %q via a non-canonical spelling", data, k)
				}
			}
		}
		if k, ok := outgoingHeaderMatcher(data); ok && k != "X-Request-Id" {
			t.Fatalf("outgoingHeaderMatcher(%q) emitted unexpected key %q", data, k)
		}
	})
}

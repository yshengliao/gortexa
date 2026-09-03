package httpcompat_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGatewayAnonymousHighByteXFF measures the attacker-observable effect from
// R1-S3-2: an unauthenticated request whose X-Forwarded-For header carries one
// non-ASCII byte must still be rejected by the auth stage (401, with a
// request id from the RequestID interceptor), the same shape as the identical
// request with a clean X-Forwarded-For (control). If grpc-gateway's
// annotateContext splices that header into outgoing gRPC metadata unchecked
// (bypassing incomingHeaderMatcher's isValidGRPCMetadataTextValue check), the
// loopback gRPC client rejects it with codes.Internal before the eight-stage
// chain (RequestID, auth, ...) ever runs, and the gateway answers 500 with an
// error body carrying no request_id.
func TestGatewayAnonymousHighByteXFF(t *testing.T) {
	ts := newGateway(t)

	// Control: honest client, clean X-Forwarded-For, no credential. Must be
	// answered by the auth stage: 401 with a request id.
	controlCode, controlBody := doHeader(t, ts, "X-Forwarded-For", "203.0.113.7")
	if controlCode != http.StatusUnauthorized {
		t.Fatalf("control (clean XFF) = %d (%s), want 401 from the auth stage", controlCode, controlBody)
	}
	if !strings.Contains(string(controlBody), `"request_id"`) {
		t.Fatalf("control (clean XFF) body = %s, want a request_id (proves the chain ran)", controlBody)
	}

	// Attack: identical request, X-Forwarded-For carries one raw 0x80 byte.
	// net/http's server accepts this as a header field value; grpc-gateway's
	// annotateContext appends it to outgoing loopback metadata without running
	// it through incomingHeaderMatcher/isValidGRPCMetadataTextValue.
	attackCode, attackBody := doHeader(t, ts, "X-Forwarded-For", "\x80")

	t.Logf("control(clean-XFF)=%d body=%s", controlCode, controlBody)
	t.Logf("attack(high-byte-XFF)=%d body=%s", attackCode, attackBody)

	// The measured, attacker-observable bypass: an anonymous request must be
	// denied at the SAME stage (auth, 401) regardless of an incidental header
	// byte, and must ALWAYS carry a request id minted by the RequestID stage.
	if attackCode != http.StatusUnauthorized || !strings.Contains(string(attackBody), `"request_id"`) {
		t.Fatalf("BYPASS: clean-XFF anonymous request -> %d %s (has request_id), "+
			"high-byte-XFF anonymous request -> %d %s (request_id present=%v): "+
			"the single non-ASCII header byte skipped RequestID/load-shed/rate-limit/auth entirely, "+
			"turning an anonymous 401 into an anonymous %d with no correlation id",
			controlCode, controlBody, attackCode, attackBody,
			strings.Contains(string(attackBody), `"request_id"`), attackCode)
	}
}

// TestGatewayHighByteSplicedHeaders covers the other two headers grpc-gateway
// splices into loopback metadata without the matcher's validity check
// (Authorization and X-Forwarded-Host), plus the multi-valued X-Forwarded-For
// case where only one of several values is malformed — the whole header must
// not be lost, and neither may it poison the metadata.
func TestGatewayHighByteSplicedHeaders(t *testing.T) {
	ts := newGateway(t)

	for _, tc := range []struct {
		name   string
		key    string
		values []string
	}{
		{"authorization high byte", "Authorization", []string{"Bearer \x80"}},
		{"authorization horizontal tab", "Authorization", []string{"Bearer a\tb"}},
		{"forwarded host high byte", "X-Forwarded-Host", []string{"ex\x80mple.test"}},
		{"forwarded for one bad value of several", "X-Forwarded-For", []string{"203.0.113.7", "\x80"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := doHeader(t, ts, tc.key, tc.values...)
			if code != http.StatusUnauthorized || !strings.Contains(string(body), `"request_id"`) {
				t.Fatalf("%s: %s = %d %s, want 401 with a request_id (the chain must run and answer)",
					tc.name, tc.key, code, body)
			}
		})
	}
}

func doHeader(t *testing.T, ts *httptest.Server, key string, values ...string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/resources/abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header[key] = values
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

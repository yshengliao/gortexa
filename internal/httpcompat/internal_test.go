package httpcompat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

func TestIncomingHeaderMatcher(t *testing.T) {
	if got, ok := incomingHeaderMatcher("Authorization"); !ok || got != auth.MetadataKey {
		t.Errorf("Authorization → %q,%v", got, ok)
	}
	if got, ok := incomingHeaderMatcher("X-Request-Id"); !ok || got != interceptor.RequestIDMetadataKey {
		t.Errorf("X-Request-Id → %q,%v", got, ok)
	}
	// unknown non-permanent header is dropped by the default matcher
	if _, ok := incomingHeaderMatcher("X-Custom-Thing"); ok {
		t.Errorf("unknown header should not be forwarded")
	}
}

// TestIncomingHeaderMatcherBlocksForwardedFor locks in the rate-limit identity
// guard: the loopback trusts x-forwarded-for metadata, and the gateway default
// matcher forwards Grpc-Metadata-* verbatim, so any spelling that would land on
// that key must be rejected — otherwise an external HTTP client could spoof its
// rate-limit bucket per request.
func TestIncomingHeaderMatcherBlocksForwardedFor(t *testing.T) {
	for _, h := range []string{
		"X-Forwarded-For",
		"Grpc-Metadata-X-Forwarded-For",
		"grpc-metadata-x-forwarded-for",
	} {
		if got, ok := incomingHeaderMatcher(h); ok {
			t.Errorf("header %q must not be forwarded, got key %q", h, got)
		}
	}
	// The Grpc-Metadata-* passthrough still works for non-trusted keys.
	if got, ok := incomingHeaderMatcher("Grpc-Metadata-X-Tenant"); !ok || !strings.EqualFold(got, "x-tenant") {
		t.Errorf("Grpc-Metadata-X-Tenant → %q,%v; want x-tenant,true", got, ok)
	}
}

func TestErrorHandlerWritesMappedBody(t *testing.T) {
	h := errorHandler(apperr.Default)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", "rid-1")
	h(req.Context(), nil, jsonMarshaler(), rec, req, apperr.New(apperr.CatNotFound, "nope"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, `"code":"not_found"`) || !contains(body, `"request_id":"rid-1"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	// disabled → passes through unchanged
	off := CORS(next, config.ServerConfig{EnableCORS: false})
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disabled CORS should not add headers")
	}

	// enabled (wildcard) → adds headers; preflight short-circuits
	onWildcard := CORS(next, config.ServerConfig{EnableCORS: true, CORSOrigins: []string{"*"}})
	rec = httptest.NewRecorder()
	onWildcard.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight = %d, origin=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// enabled (specific origin) allowed
	onSpecific := CORS(next, config.ServerConfig{EnableCORS: true, CORSOrigins: []string{"https://example.com"}})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	onSpecific.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatalf("preflight specific allowed = %d, origin=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// enabled (specific origin) denied
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://attacker.com")
	onSpecific.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight specific denied = %d, origin=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestOpenAPIHandler(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "api.json")
	_ = os.WriteFile(spec, []byte(`{"openapi":"x"}`), 0o600)

	rec := httptest.NewRecorder()
	OpenAPIHandler(spec).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"openapi":"x"}` {
		t.Fatalf("present spec = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	OpenAPIHandler(filepath.Join(dir, "missing.json")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing spec = %d, want 404", rec.Code)
	}
}

func TestOpenAPIRoute(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	dir := t.TempDir()
	spec := filepath.Join(dir, "api.json")
	_ = os.WriteFile(spec, []byte(`{"openapi":"x"}`), 0o600)

	// disabled → passthrough, /openapi.json reaches next
	off := OpenAPIRoute(next, config.ServerConfig{OpenAPI: false}, spec)
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("disabled route = %d, want passthrough", rec.Code)
	}

	// enabled → serves the spec at /openapi.json, passes everything else through
	on := OpenAPIRoute(next, config.ServerConfig{OpenAPI: true}, spec)
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"openapi":"x"}` {
		t.Fatalf("enabled route = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("other path = %d, want passthrough", rec.Code)
	}

	// enabled but spec missing → /openapi.json serves 404, other paths still pass
	missing := OpenAPIRoute(next, config.ServerConfig{OpenAPI: true}, filepath.Join(dir, "nope.json"))
	rec = httptest.NewRecorder()
	missing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing spec = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	missing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("missing-spec other path = %d, want passthrough", rec.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

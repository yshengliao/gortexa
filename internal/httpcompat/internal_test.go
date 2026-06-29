package httpcompat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	// enabled → adds headers; preflight short-circuits
	on := CORS(next, config.ServerConfig{EnableCORS: true})
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight = %d, origin=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

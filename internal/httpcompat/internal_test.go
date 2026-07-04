package httpcompat

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"github.com/yshengliao/gortexa/internal/auth"
	"github.com/yshengliao/gortexa/internal/config"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/interceptor"
)

// errReader fails every Read with a fixed error, standing in for a body whose
// read deadline has fired.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestMaxBodyBytes(t *testing.T) {
	var served bool
	h := MaxBodyBytes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A handler that reads the whole body, as the gateway decoder does.
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusRequestEntityTooLarge)
			return
		}
		served = true
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("over-declared content-length rejected up front", func(t *testing.T) {
		served = false
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, MaxRequestBytes+1)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
		if served {
			t.Fatal("handler ran despite oversize body")
		}
	})

	t.Run("under-limit body passes", func(t *testing.T) {
		served = false
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 1024)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !served {
			t.Fatalf("status = %d served = %v, want 200/true", rec.Code, served)
		}
	})

	t.Run("chunked oversize body rejected with 413", func(t *testing.T) {
		served = false
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, MaxRequestBytes+1)))
		req.ContentLength = -1 // unknown length: Content-Length guard can't catch it
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (body read and size-checked in the middleware)", rec.Code)
		}
		if served {
			t.Fatal("handler completed on an oversize chunked body")
		}
	})

	t.Run("body read error maps to 408 not 413", func(t *testing.T) {
		served = false
		// A body that errors mid-read is exactly what a fired read deadline
		// produces (os.ErrDeadlineExceeded). httptest.ResponseRecorder can't
		// arm a real deadline, so inject the read error directly to exercise the
		// non-MaxBytesError branch: it must map to 408, not 413.
		req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(errReader{os.ErrDeadlineExceeded}))
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestTimeout {
			t.Fatalf("status = %d, want 408 for a body-read error", rec.Code)
		}
		if served {
			t.Fatal("handler ran despite a body read error")
		}
	})
}

func TestClientIPMetadata(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		wantHost   string
		wantNil    bool
	}{
		{name: "host:port is split", remoteAddr: "10.0.0.7:54321", wantHost: "10.0.0.7"},
		{name: "bare host kept as-is", remoteAddr: "10.0.0.9", wantHost: "10.0.0.9"},
		{name: "empty remote addr yields nil md", remoteAddr: "", wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			md := clientIPMetadata(req.Context(), req)
			if tt.wantNil {
				if md != nil {
					t.Fatalf("md = %v, want nil", md)
				}
				return
			}
			if got := md.Get(interceptor.PeerIPMetaKey); len(got) != 1 || got[0] != tt.wantHost {
				t.Fatalf("peer-ip md = %v, want [%s]", got, tt.wantHost)
			}
		})
	}
}

// failMarshaler is a runtime.Marshaler whose Marshal always fails, to drive the
// errorHandler's marshal-error fallback (a hard 500 with a static body).
type failMarshaler struct{}

func (failMarshaler) Marshal(any) ([]byte, error)          { return nil, errors.New("boom") }
func (failMarshaler) Unmarshal([]byte, any) error          { return nil }
func (failMarshaler) NewDecoder(io.Reader) runtime.Decoder { return nil }
func (failMarshaler) NewEncoder(io.Writer) runtime.Encoder { return nil }
func (failMarshaler) ContentType(any) string               { return "application/json" }

func TestErrorHandlerMarshalFailureFallsBackTo500(t *testing.T) {
	h := errorHandler(apperr.Default)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h(req.Context(), nil, failMarshaler{}, rec, req, apperr.New(apperr.CatNotFound, "nope"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, `"code":"internal"`) {
		t.Fatalf("body = %s", body)
	}
}

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

// TestIncomingHeaderMatcherBlocksPeerIP locks in the rate-limit identity guard:
// the loopback trusts the dedicated peer-IP metadata key, and the gateway default
// matcher forwards Grpc-Metadata-* verbatim, so any spelling that would land on
// that key must be rejected — otherwise an external HTTP client could spoof its
// rate-limit bucket per request.
func TestIncomingHeaderMatcherBlocksPeerIP(t *testing.T) {
	for _, h := range []string{
		"X-Gortexa-Peer-Ip",
		"Grpc-Metadata-X-Gortexa-Peer-Ip",
		"grpc-metadata-x-gortexa-peer-ip",
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

func TestOutgoingHeaderMatcher(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantKey string
		wantOK  bool
	}{
		{
			name:    "request id becomes clean X-Request-Id",
			key:     interceptor.RequestIDMetadataKey,
			wantKey: "X-Request-Id",
			wantOK:  true,
		},
		{
			name:    "request id match is case-insensitive",
			key:     strings.ToUpper(interceptor.RequestIDMetadataKey),
			wantKey: "X-Request-Id",
			wantOK:  true,
		},
		{
			name:    "unknown key is dropped by default",
			key:     "x-tenant",
			wantKey: "",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := outgoingHeaderMatcher(tt.key)
			if got != tt.wantKey || ok != tt.wantOK {
				t.Fatalf("outgoingHeaderMatcher(%q) = %q,%v; want %q,%v", tt.key, got, ok, tt.wantKey, tt.wantOK)
			}
		})
	}
}

func TestRoutingErrorHandler(t *testing.T) {
	// A 405 routing status writes a 405 JSON body naming method_not_allowed,
	// instead of the gateway default (which reports Unimplemented / 501).
	t.Run("405 writes method_not_allowed body", func(t *testing.T) {
		h := routingErrorHandler(apperr.Default)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/x", nil)
		h(req.Context(), NewServeMux(apperr.Default), jsonMarshaler(), rec, req, http.StatusMethodNotAllowed)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q, want application/json", ct)
		}
		if body := rec.Body.String(); !contains(body, `"code":"method_not_allowed"`) {
			t.Fatalf("body = %s", body)
		}
	})

	// A non-405 routing status delegates to the gateway default handler, which
	// maps StatusNotFound to a not-found body — never the 405 branch.
	t.Run("non-405 delegates to default", func(t *testing.T) {
		h := routingErrorHandler(apperr.Default)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		h(req.Context(), NewServeMux(apperr.Default), jsonMarshaler(), rec, req, http.StatusNotFound)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404", rec.Code)
		}
		if body := rec.Body.String(); contains(body, `"method_not_allowed"`) {
			t.Fatalf("non-405 must not use the method_not_allowed branch: %s", body)
		}
	})
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

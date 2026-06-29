package kernel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func stub(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Stub", tag)
		w.WriteHeader(http.StatusOK)
	})
}

func TestMuxRouting(t *testing.T) {
	mux := newMux(stub("grpc"), stub("mcp"), stub("gateway"), stub("healthz"), stub("readyz"))

	cases := []struct {
		name     string
		method   string
		path     string
		proto2   bool
		grpcCT   bool
		wantStub string
	}{
		{"grpc over h2c", "POST", "/resource.v1.ResourceService/GetResource", true, true, "grpc"},
		{"mcp", "POST", "/mcp", false, false, "mcp"},
		{"healthz", "GET", "/healthz", false, false, "healthz"},
		{"readyz", "GET", "/readyz", false, false, "readyz"},
		{"gateway fallthrough", "GET", "/v1/resources", false, false, "gateway"},
		{"http2 non-grpc → gateway", "GET", "/v1/resources", true, false, "gateway"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			if c.proto2 {
				req.ProtoMajor = 2
			}
			if c.grpcCT {
				req.Header.Set("Content-Type", "application/grpc")
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if got := rec.Header().Get("X-Stub"); got != c.wantStub {
				t.Fatalf("routed to %q, want %q", got, c.wantStub)
			}
		})
	}
}

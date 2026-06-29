package httpcompat_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/auth"
	apperr "github.com/yshengliao/gortexa/internal/errors"
	"github.com/yshengliao/gortexa/internal/httpcompat"
	"github.com/yshengliao/gortexa/internal/logic"
	"github.com/yshengliao/gortexa/testutil"
)

func token(t *testing.T) string {
	t.Helper()
	tok, err := auth.MustNewVerifier(testutil.DefaultSecret, "gortexa").Sign("tester", []string{"admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func do(t *testing.T, method, url, body, bearer string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func newGateway(t *testing.T) *httptest.Server {
	t.Helper()
	conn := testutil.NewTestServer(t, func(s *grpc.Server) {
		resourcev1.RegisterResourceServiceServer(s, logic.NewResourceService())
	})
	mux := httpcompat.NewServeMux(apperr.Default)
	if err := resourcev1.RegisterResourceServiceHandler(context.Background(), mux, conn); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestGatewayAuthShared(t *testing.T) {
	ts := newGateway(t)
	// HTTP shares the gRPC auth path via the header matcher: no token → 401.
	if code, _ := do(t, http.MethodGet, ts.URL+"/v1/resources/abc", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET = %d, want 401", code)
	}
}

func TestGatewayCRUDVerbs(t *testing.T) {
	ts := newGateway(t)
	tok := token(t)

	// POST create (body maps to the resource field)
	code, body := do(t, http.MethodPost, ts.URL+"/v1/resources", `{"name":"alpha","owner":"u-1"}`, tok)
	if code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, body)
	}
	var created struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		t.Fatalf("create body = %s err=%v", body, err)
	}
	if created.Status != "STATUS_ACTIVE" {
		t.Fatalf("status = %q, want STATUS_ACTIVE (enum as string)", created.Status)
	}

	// GET by path param
	code, body = do(t, http.MethodGet, ts.URL+"/v1/resources/"+created.ID, "", tok)
	if code != http.StatusOK || !strings.Contains(string(body), "alpha") {
		t.Fatalf("get = %d (%s)", code, body)
	}

	// GET list (query param)
	if code, _ := do(t, http.MethodGet, ts.URL+"/v1/resources?owner=u-1", "", tok); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}

	// PATCH update (nested path + body)
	code, body = do(t, http.MethodPatch, ts.URL+"/v1/resources/"+created.ID, `{"name":"beta","owner":"u-1"}`, tok)
	if code != http.StatusOK || !strings.Contains(string(body), "beta") {
		t.Fatalf("update = %d (%s)", code, body)
	}

	// DELETE
	if code, _ := do(t, http.MethodDelete, ts.URL+"/v1/resources/"+created.ID, "", tok); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
}

func TestGatewayErrorMapping(t *testing.T) {
	ts := newGateway(t)
	tok := token(t)

	// gRPC NotFound → HTTP 404 with the shared error body shape.
	code, body := do(t, http.MethodGet, ts.URL+"/v1/resources/missing", "", tok)
	if code != http.StatusNotFound {
		t.Fatalf("missing = %d (%s), want 404", code, body)
	}
	var eb struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &eb); err != nil || eb.Code != "not_found" {
		t.Fatalf("error body = %s err=%v", body, err)
	}

	// Validation failure → 400.
	if code, _ := do(t, http.MethodPost, ts.URL+"/v1/resources", `{"owner":"u-1"}`, tok); code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d, want 400", code)
	}
}

func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }

package kernel

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/health"
)

// The 503 that takes a pod out of rotation used to write nothing anywhere, so
// an operator paged by the load balancer had no server-side record of which
// dependency was down. A healthy /readyz stays quiet — this fires only on the
// transition into not-serving, which is the event worth a line.
func TestReadyzLogsWhenNotServing(t *testing.T) {
	var buf bytes.Buffer
	app, err := New(WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	app.Health().Register("db", func(context.Context) health.State { return health.Unhealthy })

	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "readiness not serving") {
		t.Fatalf("a 503 must be logged; got:\n%s", out)
	}
	// The line is only useful if it names which check failed.
	if !strings.Contains(out, "db") {
		t.Errorf("log must name the failing check; got:\n%s", out)
	}
}

func TestReadyzStaysQuietWhenServing(t *testing.T) {
	var buf bytes.Buffer
	app, err := New(WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))), WithoutInterceptors())
	if err != nil {
		t.Fatal(err)
	}
	app.Health().Register("db", func(context.Context) health.State { return health.Healthy })

	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(buf.String(), "readiness not serving") {
		t.Fatalf("a serving readyz must not warn; got:\n%s", buf.String())
	}
}

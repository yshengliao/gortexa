package interceptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// The wrapped cause of an *apperr.Error is the only record of an Internal
// error's root cause (Recovery strips it before the client sees it), so the
// server-side rpc line must carry it.
func TestLoggerRecordsWrappedCause(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	err := apperr.Wrap(apperr.CatInternal, "load user", errors.New("pq: relation users does not exist"))
	logRPC(context.Background(), log, "/svc/M", err, 0)
	if out := buf.String(); !strings.Contains(out, `"error.cause":"pq: relation users does not exist"`) {
		t.Fatalf("log line lacks the cause: %s", out)
	}

	buf.Reset()
	logRPC(context.Background(), log, "/svc/M", apperr.New(apperr.CatNotFound, "gone"), 0)
	if out := buf.String(); strings.Contains(out, "error.cause") {
		t.Fatalf("cause attribute emitted without a cause: %s", out)
	}
}

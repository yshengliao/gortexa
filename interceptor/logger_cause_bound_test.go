package interceptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	apperr "github.com/yshengliao/gortexa/apperr"
)

// maxLoggedCauseBytes is the ceiling this test holds the ERROR record to; the
// bounded validation message is ~512 bytes, so a cause of the same order plus
// the fixed attributes comfortably fits.
const maxLoggedRecordBytes = 4096

// TestLoggerBoundsValidationCause pins the second half of the validation
// amplification fix. boundedViolations caps the client-facing message, but the
// *protovalidate.ValidationError was still wrapped as the cause, and the
// logger renders the cause in full — one line per violation. A request whose
// repeated field carries an item rule therefore produced a client message of a
// few hundred bytes and a server-side ERROR record proportional to the
// element count (50 000 violations: ~1.9 MB per request), at the per-IP rate
// limit, from any authenticated caller.
func TestLoggerBoundsValidationCause(t *testing.T) {
	base := oneViolation(t)
	verr := &protovalidate.ValidationError{Violations: make([]*protovalidate.Violation, 50_000)}
	for i := range verr.Violations {
		verr.Violations[i] = base
	}
	out := failValidation(context.Background(), nil, "/resource.v1.ResourceService/GetResource", verr)

	var buf bytes.Buffer
	logRPC(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), "/resource.v1.ResourceService/GetResource", out, 0)
	if buf.Len() > maxLoggedRecordBytes {
		t.Fatalf("ERROR record is %d bytes for a %d-violation request; the logged cause must be bounded", buf.Len(), len(verr.Violations))
	}
	if !strings.Contains(buf.String(), "error.cause") {
		t.Fatalf("validation failures must still log a cause: %s", buf.String())
	}
}

// TestLoggerTruncatesAnyOversizedCause is the belt to the braces above: a
// consumer's own handler may wrap an unbounded cause (a driver error that
// echoes a request value), and the logger is the last place to cap it.
func TestLoggerTruncatesAnyOversizedCause(t *testing.T) {
	huge := errors.New(strings.Repeat("x", 1<<20))
	err := apperr.Wrap(apperr.CatInternal, "load user", huge)

	var buf bytes.Buffer
	logRPC(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), "/svc/M", err, 0)
	if buf.Len() > maxLoggedRecordBytes {
		t.Fatalf("ERROR record is %d bytes; the logged cause must be truncated", buf.Len())
	}
	if !strings.Contains(buf.String(), "xxxx") {
		t.Fatalf("truncated cause must keep its head: %s", buf.String())
	}
}

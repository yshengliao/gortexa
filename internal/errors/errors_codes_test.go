package errors_test

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

// Codes that previously fell through to Internal/500 now map to dedicated statuses.
func TestPreviouslyUnmappedCodes(t *testing.T) {
	cases := []struct {
		code     codes.Code
		wantHTTP int
		wantCat  string
	}{
		{codes.Canceled, 499, "canceled"},
		{codes.OutOfRange, http.StatusBadRequest, "out_of_range"},
		{codes.DataLoss, http.StatusInternalServerError, "data_loss"},
	}
	for _, c := range cases {
		err := status.Error(c.code, "downstream detail")
		gotHTTP, body := apperr.ToHTTP(err)
		if gotHTTP != c.wantHTTP || body.Code != c.wantCat {
			t.Errorf("%v → http=%d cat=%q, want %d/%q", c.code, gotHTTP, body.Code, c.wantHTTP, c.wantCat)
		}
		if got := apperr.ToGRPCStatus(err).Code(); got != c.code {
			t.Errorf("%v → grpc code %v, want round-trip", c.code, got)
		}
	}
}

// Genuinely unknown/unmapped inputs still collapse to Internal without leaking detail.
func TestStillInternalNoLeak(t *testing.T) {
	for _, err := range []error{
		status.Error(codes.Unknown, "secret detail"),
		apperr.New("no_such_category", "boom"),
	} {
		httpCode, body := apperr.ToHTTP(err)
		if httpCode != http.StatusInternalServerError || body.Code != string(apperr.CatInternal) {
			t.Errorf("err %v → %d/%q, want 500/internal", err, httpCode, body.Code)
		}
		if body.Message != "internal error" {
			t.Errorf("internal must not leak detail, got %q", body.Message)
		}
	}
}

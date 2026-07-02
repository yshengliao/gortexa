package auth_test

import (
	"testing"

	"github.com/yshengliao/gortexa/internal/auth"
)

func TestMustNewVerifier(t *testing.T) {
	cases := []struct {
		name      string
		secret    []byte
		wantPanic bool
	}{
		{"valid secret returns verifier", secret, false},
		{"short secret panics", []byte("too-short"), true},
		{"nil secret panics", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if tc.wantPanic && r == nil {
					t.Error("expected panic, got none")
				}
				if !tc.wantPanic && r != nil {
					t.Errorf("unexpected panic: %v", r)
				}
			}()
			if v := auth.MustNewVerifier(tc.secret, "gortexa"); v == nil {
				t.Error("MustNewVerifier returned nil verifier")
			}
		})
	}
}

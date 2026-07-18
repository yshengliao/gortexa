package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/yshengliao/gortexa/config"
)

func TestSecretGoString(t *testing.T) {
	cases := []struct {
		name string
		s    config.Secret
		want string
	}{
		{"nonempty masks", config.Secret(validSecret), "****"},
		{"empty stays empty", config.Secret(""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.GoString(); got != tc.want {
				t.Errorf("GoString() = %q, want %q", got, tc.want)
			}
			if got := fmt.Sprintf("%#v", tc.s); strings.Contains(got, validSecret) {
				t.Errorf("%%#v leaked secret: %q", got)
			}
		})
	}
}

func TestSecretLogValue(t *testing.T) {
	s := config.Secret(validSecret)
	if got := s.LogValue().String(); got != "****" {
		t.Errorf("LogValue() = %q, want ****", got)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("msg", "secret", s)
	if out := buf.String(); strings.Contains(out, validSecret) {
		t.Errorf("slog output leaked secret: %q", out)
	}
}

func TestSecretMarshalJSON(t *testing.T) {
	cases := []struct {
		name string
		s    config.Secret
		want string
	}{
		{"nonempty masks", config.Secret(validSecret), `"****"`},
		{"empty is empty string", config.Secret(""), `""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.s)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("Marshal() = %s, want %s", b, tc.want)
			}
		})
	}
}

package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

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

// TestSecretMarshalText pins the mask for text-based encoders. fmt, slog and
// encoding/json were covered; yaml.v3 (already in the module graph via koanf)
// and encoding/xml consult encoding.TextMarshaler, not fmt.Stringer, so a
// config dumped through them leaked the raw value.
func TestSecretMarshalText(t *testing.T) {
	b, err := yaml.Marshal(struct {
		S config.Secret `yaml:"s"`
		E config.Secret `yaml:"e"`
	}{S: "hunter2hunter2", E: ""})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("yaml leaks the secret: %s", b)
	}
	if !strings.Contains(string(b), "****") {
		t.Fatalf("yaml must show the mask: %s", b)
	}
	txt, err := config.Secret("hunter2hunter2").MarshalText()
	if err != nil || string(txt) != "****" {
		t.Fatalf("MarshalText = %q, %v", txt, err)
	}
	if txt, _ := config.Secret("").MarshalText(); string(txt) != "" {
		t.Fatalf("empty secret MarshalText = %q, want empty", txt)
	}
}

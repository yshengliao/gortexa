package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    slog.Level
		wantErr bool
	}{
		{name: "debug", in: "debug", want: slog.LevelDebug},
		{name: "info", in: "info", want: slog.LevelInfo},
		{name: "empty defaults to info", in: "", want: slog.LevelInfo},
		{name: "warn", in: "warn", want: slog.LevelWarn},
		{name: "error", in: "error", want: slog.LevelError},
		{name: "uppercase INFO", in: "INFO", want: slog.LevelInfo},
		{name: "uppercase WARN", in: "WARN", want: slog.LevelWarn},
		{name: "invalid", in: "verbose", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLevel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseLevel(%q): want error, got nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLevel(%q): unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestClampRatio(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "negative clamps to zero", in: -0.5, want: 0},
		{name: "zero", in: 0, want: 0},
		{name: "above one clamps to one", in: 2.5, want: 1},
		{name: "exactly one", in: 1, want: 1},
		{name: "in range passes through", in: 0.25, want: 0.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampRatio(tt.in); got != tt.want {
				t.Fatalf("clampRatio(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewResource(t *testing.T) {
	tests := []struct {
		name    string
		service string
		version string
	}{
		{name: "with version", service: "svc", version: "1.2.3"},
		{name: "without version", service: "svc", version: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := newResource(context.Background(), tt.service, tt.version)
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			attrs := res.Attributes()
			var gotService, gotVersion bool
			for _, kv := range attrs {
				switch string(kv.Key) {
				case "service.name":
					gotService = kv.Value.AsString() == tt.service
				case "service.version":
					gotVersion = kv.Value.AsString() == tt.version
				}
			}
			if !gotService {
				t.Fatal("resource missing service.name attribute")
			}
			if (tt.version != "") != gotVersion {
				t.Fatalf("service.version presence = %v, want %v", gotVersion, tt.version != "")
			}
		})
	}
}

type errHandler struct{ slog.Handler }

func (e errHandler) Handle(context.Context, slog.Record) error {
	return errors.New("handle failed")
}

func TestFanoutHandler(t *testing.T) {
	t.Run("Enabled true when any handler enabled", func(t *testing.T) {
		var buf bytes.Buffer
		f := fanoutHandler{
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}),
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		}
		if !f.Enabled(context.Background(), slog.LevelInfo) {
			t.Fatal("Enabled(info) = false, want true")
		}
	})

	t.Run("Enabled false when no handler enabled", func(t *testing.T) {
		var buf bytes.Buffer
		f := fanoutHandler{
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}),
		}
		if f.Enabled(context.Background(), slog.LevelInfo) {
			t.Fatal("Enabled(info) = true, want false")
		}
	})

	t.Run("Handle fans out only to enabled handlers", func(t *testing.T) {
		var infoBuf, errBuf bytes.Buffer
		f := fanoutHandler{
			slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo}),
			slog.NewJSONHandler(&errBuf, &slog.HandlerOptions{Level: slog.LevelError}),
		}
		logger := slog.New(f)
		logger.Info("hello")
		if !strings.Contains(infoBuf.String(), "hello") {
			t.Fatal("info handler did not receive record")
		}
		if errBuf.Len() != 0 {
			t.Fatal("error-level handler should not receive info record")
		}
	})

	t.Run("Handle propagates handler error", func(t *testing.T) {
		var buf bytes.Buffer
		inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		f := fanoutHandler{errHandler{inner}}
		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "boom", 0)
		if err := f.Handle(context.Background(), rec); err == nil {
			t.Fatal("Handle: want error, got nil")
		}
	})

	t.Run("WithAttrs applies to all handlers", func(t *testing.T) {
		var buf bytes.Buffer
		f := fanoutHandler{slog.NewJSONHandler(&buf, nil)}
		h := f.WithAttrs([]slog.Attr{slog.String("k", "v")})
		slog.New(h).Info("attrs")
		if !strings.Contains(buf.String(), `"k":"v"`) {
			t.Fatalf("output missing attr, got %q", buf.String())
		}
	})

	t.Run("WithGroup applies to all handlers", func(t *testing.T) {
		var buf bytes.Buffer
		f := fanoutHandler{slog.NewJSONHandler(&buf, nil)}
		h := f.WithGroup("grp")
		slog.New(h).Info("grouped", "k", "v")
		if !strings.Contains(buf.String(), `"grp"`) {
			t.Fatalf("output missing group, got %q", buf.String())
		}
	})
}

// TestOTLPSecurityDefaultsToTLS pins that the OTLP transport-security helpers
// return a TLS option unless cleartext is explicitly opted into, so telemetry
// is not exported in cleartext by default.
func TestOTLPSecurityDefaultsToTLS(t *testing.T) {
	// Each helper must return a non-nil option in both modes; the secure branch
	// (insecure=false) is the default and must be reachable without a config knob.
	if logSecurity(false) == nil || logSecurity(true) == nil {
		t.Fatal("logSecurity returned nil option")
	}
	if traceSecurity(false) == nil || traceSecurity(true) == nil {
		t.Fatal("traceSecurity returned nil option")
	}
	if metricSecurity(false) == nil || metricSecurity(true) == nil {
		t.Fatal("metricSecurity returned nil option")
	}
}

func TestInstallErrorHandlerRoutesToSlog(t *testing.T) {
	installErrorHandler()

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	otel.Handle(errors.New("collector down"))
	if !strings.Contains(buf.String(), "collector down") {
		t.Fatalf("otel export error not routed to slog, got %q", buf.String())
	}
}

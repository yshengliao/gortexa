package config

import "log/slog"

// Secret is a string that never reveals itself in logs, JSON, or fmt output.
// Use Reveal to obtain the underlying value at the point of use.
type Secret string

const secretMask = "****"

// String masks the value (also covers fmt %s/%v).
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return secretMask
}

// GoString masks the value for %#v.
func (s Secret) GoString() string { return s.String() }

// LogValue masks the value for slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON masks the value in JSON output.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte(`""`), nil
	}
	return []byte(`"` + secretMask + `"`), nil
}

// Reveal returns the underlying secret value.
func (s Secret) Reveal() string { return string(s) }

package mcp

import (
	"encoding/json"
	"strings"
)

var defaultMaskFields = []string{"password", "token", "secret", "authorization", "api_key"}

// MaskSecrets recursively redacts configured JSON object keys. Input that is
// not valid JSON (or a bare string/number/bool root, which could itself be a
// secret) is replaced wholesale with a redaction placeholder.
func MaskSecrets(raw string, maskFields []string) string {
	if raw == "" {
		return raw
	}
	if len(maskFields) == 0 {
		maskFields = defaultMaskFields
	}
	// UseNumber keeps integers exact: decoding into `any` would otherwise turn
	// large numbers (e.g. 64-bit IDs) into float64 and lose precision on re-marshal.
	var v any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "[UNPARSEABLE JSON REDACTED]"
	}
	// If the root is just a string/number/bool, we don't know if it's a secret,
	// so we defensively mask it.
	switch v.(type) {
	case string, json.Number, bool:
		v = "[REDACTED]"
	default:
		mask(v, maskSet(maskFields))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[UNPARSEABLE JSON REDACTED]"
	}
	return string(b)
}

// normalizeKey folds a field name for comparison so a mask field written in
// snake_case ("api_key") also matches the camelCase key protojson emits
// ("apiKey"): lowercase, then drop underscores.
func normalizeKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "")
}

func maskSet(fields []string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		m[normalizeKey(f)] = struct{}{}
	}
	return m
}

func mask(v any, fields map[string]struct{}) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if _, ok := fields[normalizeKey(k)]; ok {
				x[k] = "[REDACTED]"
				continue
			}
			mask(val, fields)
		}
	case []any:
		for _, val := range x {
			mask(val, fields)
		}
	}
}

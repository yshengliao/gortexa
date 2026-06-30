package mcp

import (
	"encoding/json"
	"strings"
)

var defaultMaskFields = []string{"password", "token", "secret", "authorization", "api_key"}

// MaskSecrets recursively redacts configured JSON object keys. Invalid JSON is returned unchanged.
func MaskSecrets(raw string, maskFields []string) string {
	if raw == "" {
		return raw
	}
	if len(maskFields) == 0 {
		maskFields = defaultMaskFields
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	mask(v, maskSet(maskFields))
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

func maskSet(fields []string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		m[strings.ToLower(f)] = struct{}{}
	}
	return m
}

func mask(v any, fields map[string]struct{}) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if _, ok := fields[strings.ToLower(k)]; ok {
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

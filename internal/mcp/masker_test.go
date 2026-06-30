package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskSecretsPreservesLargeIntegers(t *testing.T) {
	// 2^53+1 cannot be represented exactly as float64; UseNumber must preserve it.
	out := MaskSecrets(`{"id":9007199254740993,"password":"p"}`, nil)
	if !strings.Contains(out, "9007199254740993") {
		t.Errorf("large integer lost precision: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("secret not masked: %s", out)
	}
}

func TestMaskSecretsPassthrough(t *testing.T) {
	if got := MaskSecrets("", nil); got != "" {
		t.Errorf("empty: got %q", got)
	}
	if got := MaskSecrets("not json", nil); got != "[UNPARSEABLE JSON REDACTED]" {
		t.Errorf("invalid JSON should be redacted: got %q", got)
	}
	if got := MaskSecrets(`"just a string"`, nil); got != `"[REDACTED]"` {
		t.Errorf("bare string JSON should be redacted: got %q", got)
	}
}

func TestMaskSecretsRedacts(t *testing.T) {
	// Default fields, case-insensitive, nested objects, and arrays of objects.
	in := `{"Password":"p","Outer":{"api_key":"k","keep":"v"},"list":[{"secret":"s"},{"ok":1}],"keep":"v"}`
	var m map[string]any
	if err := json.Unmarshal([]byte(MaskSecrets(in, nil)), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if m["Password"] != "[REDACTED]" {
		t.Errorf("case-insensitive top-level secret not masked: %v", m["Password"])
	}
	if m["keep"] != "v" {
		t.Errorf("non-secret changed: %v", m["keep"])
	}
	outer := m["Outer"].(map[string]any)
	if outer["api_key"] != "[REDACTED]" {
		t.Errorf("nested secret not masked: %v", outer["api_key"])
	}
	if outer["keep"] != "v" {
		t.Errorf("nested non-secret changed: %v", outer["keep"])
	}
	list := m["list"].([]any)
	if list[0].(map[string]any)["secret"] != "[REDACTED]" {
		t.Errorf("secret inside array object not masked")
	}
	if list[1].(map[string]any)["ok"].(float64) != 1 {
		t.Errorf("non-secret inside array object changed")
	}
}

func TestMaskSecretsCustomFieldsOverrideDefaults(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(MaskSecrets(`{"password":"p","custom":"c"}`, []string{"custom"})), &m); err != nil {
		t.Fatal(err)
	}
	if m["custom"] != "[REDACTED]" {
		t.Errorf("custom field not masked: %v", m["custom"])
	}
	if m["password"] != "p" {
		t.Errorf("default field masked despite explicit custom list: %v", m["password"])
	}
}

package mcp

import (
	"encoding/json"
	"testing"
)

// TestMaskSecretsDefaultFloorCoversCommonVariants pins the widened default
// mask set and the key normalisation. Matching was exact after lower-casing
// and dropping underscores, so the floor caught password/token/secret but not
// accessToken, refreshToken, clientSecret, privateKey, apiToken, cookie or a
// json_name with a hyphen (api-key). The floor's stated intent is that adding a
// custom field can never silently drop masking of the common names, so the
// common names have to be in it.
func TestMaskSecretsDefaultFloorCoversCommonVariants(t *testing.T) {
	in := `{"accessToken":"a","refresh_token":"b","idToken":"c","client_secret":"d","secretKey":"e",
	        "private_key":"f","apiToken":"g","api-key":"h","passwordHash":"i","cookie":"j",
	        "credentials":"k","sessionId":"l","Authorization":"m","nested":{"x-api-key":"n"},
	        "name":"keep","count":3}`
	var m map[string]any
	if err := json.Unmarshal([]byte(MaskSecrets(in, nil)), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"accessToken", "refresh_token", "idToken", "client_secret", "secretKey",
		"private_key", "apiToken", "api-key", "passwordHash", "cookie", "credentials", "sessionId", "Authorization"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v, want redacted", k, m[k])
		}
	}
	if nested, _ := m["nested"].(map[string]any); nested["x-api-key"] != "[REDACTED]" {
		t.Errorf("x-api-key = %v, want redacted", nested["x-api-key"])
	}
	if m["name"] != "keep" || m["count"] != float64(3) {
		t.Errorf("non-secret fields altered: %v", m)
	}
}

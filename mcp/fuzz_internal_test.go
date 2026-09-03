package mcp

import (
	"encoding/json"
	"testing"
)

func FuzzValidateRPCRequest(f *testing.F) {
	seeds := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":""}`,
		`{"jsonrpc":"2.0","id":1,"method":123}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return
		}
		req, id, rerr := validateRPCRequest(fields)
		if rerr == nil {
			if len(id) != 0 && !validRPCID(id) {
				t.Fatalf("rerr nil but id invalid: %q", id)
			}
			_ = req
			return
		}
		switch rerr.Code {
		case -32600, -32700, -32602:
		default:
			t.Fatalf("unexpected error code %d", rerr.Code)
		}
	})
}

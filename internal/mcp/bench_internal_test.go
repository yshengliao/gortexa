package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/yshengliao/gortexa/gen/resource/v1" // register the resource descriptors

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func benchBridge(b *testing.B) *Bridge {
	b.Helper()
	svcs, err := ServiceDescriptors("resource.v1.ResourceService")
	if err != nil {
		b.Fatal(err)
	}
	br, err := NewBridge(nil, svcs, apperr.Default)
	if err != nil {
		b.Fatal(err)
	}
	return br
}

// tools/list now returns the cached, pre-downgraded slice (O(1)).
func BenchmarkToolsListMemoized(b *testing.B) {
	br := benchBridge(b)
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = br.dispatch(r, req)
	}
}

var sinkMCPTool MCPTool

// The per-tool downgrade work that memoization eliminates from the tools/list path.
func BenchmarkDowngradeMCP(b *testing.B) {
	br := benchBridge(b)
	ir := br.tools[br.order[0]]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkMCPTool = DowngradeMCP(ir)
	}
	_ = sinkMCPTool
}

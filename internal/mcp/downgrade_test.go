package mcp_test

import (
	"encoding/json"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/mcp"
	"github.com/yshengliao/gortexa/testutil"
)

func irTools(t *testing.T) []mcp.ToolIR {
	t.Helper()
	tools, err := mcp.BuildIR(resourcev1.File_resource_v1_resource_proto.Services().Get(0))
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestDowngradeGolden(t *testing.T) {
	tools := irTools(t)

	mcpTools := make([]mcp.MCPTool, len(tools))
	openaiTools := make([]mcp.OpenAIFunction, len(tools))
	geminiTools := make([]mcp.GeminiFunctionDeclaration, len(tools))
	for i, ir := range tools {
		mcpTools[i] = mcp.DowngradeMCP(ir)
		openaiTools[i] = mcp.DowngradeOpenAI(ir)
		geminiTools[i] = mcp.DowngradeGemini(ir)
	}

	marshal := func(v any) []byte {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	testutil.Golden(t, "downgrade_mcp", marshal(mcpTools))
	testutil.Golden(t, "downgrade_openai", marshal(openaiTools))
	testutil.Golden(t, "downgrade_gemini", marshal(geminiTools))
}

func TestOpenAIStrictInvariants(t *testing.T) {
	// Strict mode: object schemas must forbid additional properties and list
	// every property as required.
	for _, ir := range irTools(t) {
		fn := mcp.DowngradeOpenAI(ir)
		p := fn.Function.Parameters
		if p == nil {
			t.Fatalf("%s: parameters nil", ir.Name)
		}
		// These tool inputs are closed messages (no proto map / Struct), so strict
		// mode must forbid undeclared keys: additionalProperties == false.
		if ap, ok := p.AdditionalProperties.(bool); !ok || ap {
			t.Fatalf("%s: additionalProperties must be false, got %#v", ir.Name, p.AdditionalProperties)
		}
		if p.Required == nil {
			t.Fatalf("%s: object schema must always emit required (even empty)", ir.Name)
		}
		if len(p.Properties) != len(*p.Required) {
			t.Fatalf("%s: strict requires all %d props required, got %d", ir.Name, len(p.Properties), len(*p.Required))
		}
	}
}

package mcp_test

import (
	"encoding/json"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/mcp"
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

	mcpTools := make([]mcp.MCPTool, 0, len(tools))
	openaiTools := make([]mcp.OpenAIFunction, 0, len(tools))
	geminiTools := make([]mcp.GeminiFunctionDeclaration, 0, len(tools))
	for _, ir := range tools {
		mcpTools = append(mcpTools, mcp.DowngradeMCP(ir))
		openaiTools = append(openaiTools, mcp.DowngradeOpenAI(ir))
		geminiTools = append(geminiTools, mcp.DowngradeGemini(ir))
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

// Strict mode lists every property as required, so a property not marked
// ai_field.required must accept null; otherwise the model has to invent a
// server-populated id/createdAt or overwrite an optional field on every call.
func TestOpenAIStrictNonRequiredFieldsAreNullable(t *testing.T) {
	byName := map[string]mcp.OpenAIFunction{}
	for _, ir := range irTools(t) {
		byName[ir.Name] = mcp.DowngradeOpenAI(ir)
	}
	typeJSON := func(s *mcp.OpenAISchema) string {
		b, err := json.Marshal(s.Type)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	res := byName["create_resource"].Function.Parameters.Properties["resource"]
	if got := typeJSON(res.Properties["createdAt"]); got != `["string","null"]` {
		t.Errorf("createdAt type = %s, want [\"string\",\"null\"]", got)
	}
	status := res.Properties["status"]
	if got := typeJSON(status); got != `["string","null"]` || status.Enum[len(status.Enum)-1] != nil {
		t.Errorf("status type = %s enum = %v, want nullable with null in enum", got, status.Enum)
	}
	// ai_field.required stays a plain, non-null type.
	if got := typeJSON(byName["get_resource"].Function.Parameters.Properties["id"]); got != `"string"` {
		t.Errorf("required id type = %s, want \"string\"", got)
	}
}

// TestOpenAIStrictValidateRequiredFieldsStayNonNull guards the other side of the
// nullable widening: a field buf.validate already requires (required = true, or
// a non-optional string with min_len >= 1) must stay a plain type, so strict
// decoding still forces the model to supply it rather than emit null and fail
// with InvalidArgument. The MCP `required` list is still ai_field-driven.
func TestOpenAIStrictValidateRequiredFieldsStayNonNull(t *testing.T) {
	byName := map[string]mcp.OpenAIFunction{}
	mcpByName := map[string]mcp.MCPTool{}
	for _, ir := range irTools(t) {
		byName[ir.Name] = mcp.DowngradeOpenAI(ir)
		mcpByName[ir.Name] = mcp.DowngradeMCP(ir)
	}
	typeJSON := func(s *mcp.OpenAISchema) string {
		t.Helper()
		b, err := json.Marshal(s.Type)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name string
		got  *mcp.OpenAISchema
		want string
	}{
		{"delete_resource.id (min_len 1)", byName["delete_resource"].Function.Parameters.Properties["id"], `"string"`},
		{"create_resource.resource (required)", byName["create_resource"].Function.Parameters.Properties["resource"], `"object"`},
		{"create_resource.resource.name (min_len 1)", byName["create_resource"].Function.Parameters.Properties["resource"].Properties["name"], `"string"`},
		// Not validate-required: still nullable.
		{"create_resource.resource.id (server-populated)", byName["create_resource"].Function.Parameters.Properties["resource"].Properties["id"], `["string","null"]`},
	}
	for _, c := range cases {
		if got := typeJSON(c.got); got != c.want {
			t.Errorf("%s type = %s, want %s", c.name, got, c.want)
		}
	}
	if req := mcpByName["delete_resource"].InputSchema.Required; len(req) != 0 {
		t.Errorf("MCP delete_resource required = %v, want unchanged (ai_field-driven, empty)", req)
	}
}

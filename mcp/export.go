package mcp

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ExportSchemas renders the gortexa.ai.v1 tool schemas of svcs in the given provider
// format ("mcp", "openai" or "gemini") as indented JSON. Each envelope matches
// the provider's registration payload — an MCP tools/list result, an OpenAI
// tools array, and a Gemini tools entry with function_declarations — so the
// output can be pasted into the target API without reshaping.
func ExportSchemas(format string, svcs []protoreflect.ServiceDescriptor) ([]byte, error) {
	var irs []ToolIR
	seen := map[string]struct{}{}
	for _, svc := range svcs {
		tools, err := BuildIR(svc)
		if err != nil {
			return nil, err
		}
		for _, ir := range tools {
			// Same rule as NewBridge: a name collision would refuse to serve at
			// runtime, so exporting it silently would hand out a schema the
			// bridge can never back.
			if _, dup := seen[ir.Name]; dup {
				return nil, fmt.Errorf("mcp: duplicate tool name %q across services", ir.Name)
			}
			seen[ir.Name] = struct{}{}
			irs = append(irs, ir)
		}
	}

	var payload any
	switch format {
	case "mcp":
		tools := make([]MCPTool, 0, len(irs))
		for _, ir := range irs {
			tools = append(tools, DowngradeMCP(ir))
		}
		payload = map[string]any{"tools": tools}
	case "openai":
		tools := make([]OpenAIFunction, 0, len(irs))
		for _, ir := range irs {
			tools = append(tools, DowngradeOpenAI(ir))
		}
		payload = map[string]any{"tools": tools}
	case "gemini":
		decls := make([]GeminiFunctionDeclaration, 0, len(irs))
		for _, ir := range irs {
			decls = append(decls, DowngradeGemini(ir))
		}
		payload = map[string]any{"tools": []map[string]any{{"function_declarations": decls}}}
	default:
		return nil, fmt.Errorf("mcp: unknown export format %q (want mcp, openai or gemini)", format)
	}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

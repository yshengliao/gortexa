package mcp

import "sort"

// MCPTool is the tools/list entry for the MCP protocol.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema *JSONSchema     `json:"inputSchema"`
	Annotations *MCPAnnotations `json:"annotations,omitempty"`
}

// MCPAnnotations carries MCP behavioral hints.
type MCPAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint,omitempty"`
	DestructiveHint bool `json:"destructiveHint,omitempty"`
}

// DowngradeMCP renders the IR as an MCP tool.
func DowngradeMCP(ir ToolIR) MCPTool {
	t := MCPTool{Name: ir.Name, Description: ir.Description, InputSchema: ir.InputSchema}
	if ir.ReadOnly || ir.Destructive {
		t.Annotations = &MCPAnnotations{ReadOnlyHint: ir.ReadOnly, DestructiveHint: ir.Destructive}
	}
	return t
}

// OpenAIFunction is the strict-mode function-tool shape for the OpenAI API.
type OpenAIFunction struct {
	Type     string         `json:"type"` // "function"
	Function OpenAIFuncBody `json:"function"`
}

// OpenAIFuncBody is the function body of an OpenAI tool.
type OpenAIFuncBody struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Strict      bool          `json:"strict"`
	Parameters  *OpenAISchema `json:"parameters"`
}

// OpenAISchema mirrors JSONSchema but, for strict mode, lists every property as
// required and forbids additional properties.
type OpenAISchema struct {
	Type                 string                   `json:"type"`
	Description          string                   `json:"description,omitempty"`
	Properties           map[string]*OpenAISchema `json:"properties,omitempty"`
	Required             []string                 `json:"required,omitempty"`
	Items                *OpenAISchema            `json:"items,omitempty"`
	Enum                 []string                 `json:"enum,omitempty"`
	AdditionalProperties *bool                    `json:"additionalProperties,omitempty"`
}

// DowngradeOpenAI renders the IR as an OpenAI strict function tool.
func DowngradeOpenAI(ir ToolIR) OpenAIFunction {
	return OpenAIFunction{
		Type: "function",
		Function: OpenAIFuncBody{
			Name:        ir.Name,
			Description: ir.Description,
			Strict:      true,
			Parameters:  toOpenAISchema(ir.InputSchema),
		},
	}
}

func toOpenAISchema(s *JSONSchema) *OpenAISchema {
	if s == nil {
		return nil
	}
	out := &OpenAISchema{Type: s.Type, Description: s.Description, Enum: s.Enum}
	if s.Items != nil {
		out.Items = toOpenAISchema(s.Items)
	}
	if s.Type == "object" {
		// Strict mode: additionalProperties:false and every property required.
		no := false
		out.AdditionalProperties = &no
		if len(s.Properties) > 0 {
			out.Properties = make(map[string]*OpenAISchema, len(s.Properties))
			names := make([]string, len(s.Properties))
			i := 0
			for name, ps := range s.Properties {
				out.Properties[name] = toOpenAISchema(ps)
				names[i] = name
				i++
			}
			sort.Strings(names)
			out.Required = names
		}
	}
	return out
}

// GeminiFunctionDeclaration is the Gemini function-calling shape.
type GeminiFunctionDeclaration struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Parameters  *GeminiSchema `json:"parameters"`
}

// GeminiSchema is the Gemini-compatible schema subset (no additionalProperties).
type GeminiSchema struct {
	Type        string                   `json:"type"`
	Description string                   `json:"description,omitempty"`
	Properties  map[string]*GeminiSchema `json:"properties,omitempty"`
	Required    []string                 `json:"required,omitempty"`
	Items       *GeminiSchema            `json:"items,omitempty"`
	Enum        []string                 `json:"enum,omitempty"`
}

// DowngradeGemini renders the IR as a Gemini FunctionDeclaration.
func DowngradeGemini(ir ToolIR) GeminiFunctionDeclaration {
	return GeminiFunctionDeclaration{
		Name:        ir.Name,
		Description: ir.Description,
		Parameters:  toGeminiSchema(ir.InputSchema),
	}
}

func toGeminiSchema(s *JSONSchema) *GeminiSchema {
	if s == nil {
		return nil
	}
	out := &GeminiSchema{Type: s.Type, Description: s.Description, Required: s.Required, Enum: s.Enum}
	if s.Items != nil {
		out.Items = toGeminiSchema(s.Items)
	}
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]*GeminiSchema, len(s.Properties))
		for name, ps := range s.Properties {
			out.Properties[name] = toGeminiSchema(ps)
		}
	}
	return out
}

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
	Type        string                   `json:"type"`
	Description string                   `json:"description,omitempty"`
	Properties  map[string]*OpenAISchema `json:"properties,omitempty"`
	// Required is a pointer so an object with no properties still emits
	// `"required": []` (OpenAI strict mode), while non-object schemas omit it.
	Required *[]string     `json:"required,omitempty"`
	Items    *OpenAISchema `json:"items,omitempty"`
	Enum     []string      `json:"enum,omitempty"`
	// AdditionalProperties is `false` for a closed object (strict mode), a
	// *OpenAISchema for a proto map's value, or `true` for a free-form Struct.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
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
		// Strict mode: every declared property is required (an empty object still
		// emits `required: []`). A proto map preserves its value schema as
		// additionalProperties; a free-form Struct stays open; a closed message
		// forbids undeclared keys.
		switch ap := s.AdditionalProperties.(type) {
		case *JSONSchema:
			out.AdditionalProperties = toOpenAISchema(ap)
		case bool:
			out.AdditionalProperties = ap
		default:
			out.AdditionalProperties = false
		}
		names := []string{}
		if len(s.Properties) > 0 {
			out.Properties = make(map[string]*OpenAISchema, len(s.Properties))
			for name, ps := range s.Properties {
				out.Properties[name] = toOpenAISchema(ps)
				names = append(names, name)
			}
			sort.Strings(names)
		}
		out.Required = &names
	}
	return out
}

// GeminiFunctionDeclaration is the Gemini function-calling shape.
type GeminiFunctionDeclaration struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Parameters  *GeminiSchema `json:"parameters"`
}

// GeminiSchema is the Gemini-compatible schema subset.
type GeminiSchema struct {
	Type        string                   `json:"type"`
	Description string                   `json:"description,omitempty"`
	Properties  map[string]*GeminiSchema `json:"properties,omitempty"`
	Required    []string                 `json:"required,omitempty"`
	Items       *GeminiSchema            `json:"items,omitempty"`
	Enum        []string                 `json:"enum,omitempty"`
	// AdditionalProperties carries a proto map's value schema (*GeminiSchema) or
	// `true` for a free-form Struct, mirroring the MCP/OpenAI downgrades so map
	// and Struct fields aren't silently flattened to a closed object.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
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
	switch ap := s.AdditionalProperties.(type) {
	case *JSONSchema:
		out.AdditionalProperties = toGeminiSchema(ap)
	case bool:
		out.AdditionalProperties = ap
	}
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]*GeminiSchema, len(s.Properties))
		for name, ps := range s.Properties {
			out.Properties[name] = toGeminiSchema(ps)
		}
	}
	return out
}

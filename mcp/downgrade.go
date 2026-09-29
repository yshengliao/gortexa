package mcp

import (
	"slices"
	"sort"
	"strings"
)

// MCPTool is the tools/list entry for the MCP protocol.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema *JSONSchema     `json:"inputSchema"`
	Annotations *MCPAnnotations `json:"annotations,omitempty"`
}

// MCPAnnotations carries MCP behavioral hints. Both are always serialized: MCP
// defaults an absent destructiveHint to true, so omitting a false one would
// mark every mutating, non-destructive tool as destructive.
type MCPAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
}

// DowngradeMCP renders the IR as an MCP tool.
func DowngradeMCP(ir ToolIR) MCPTool {
	return MCPTool{
		Name:        ir.Name,
		Description: ir.Description,
		InputSchema: ir.InputSchema,
		Annotations: &MCPAnnotations{ReadOnlyHint: ir.ReadOnly, DestructiveHint: ir.Destructive},
	}
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
	// Type is a string, or ["<type>","null"] for a property the model may leave
	// unset (see nullable).
	Type        any                      `json:"type"`
	Description string                   `json:"description,omitempty"`
	Properties  map[string]*OpenAISchema `json:"properties,omitempty"`
	// Required is a pointer so an object with no properties still emits
	// `"required": []` (OpenAI strict mode), while non-object schemas omit it.
	Required *[]string     `json:"required,omitempty"`
	Items    *OpenAISchema `json:"items,omitempty"`
	Enum     []any         `json:"enum,omitempty"`
	// AdditionalProperties is `false` for a closed object (strict mode), a
	// *OpenAISchema for a proto map's value, or `true` for a free-form Struct.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
}

// DowngradeOpenAI renders the IR as an OpenAI function tool. Strict mode requires
// every property to be required and forbids open objects (additionalProperties
// other than false). A message containing a proto oneof or a map/Struct field
// cannot satisfy strict, so such tools are emitted non-strict rather than as a
// schema the OpenAI API rejects at registration time.
func DowngradeOpenAI(ir ToolIR) OpenAIFunction {
	return OpenAIFunction{
		Type: "function",
		Function: OpenAIFuncBody{
			Name:        ir.Name,
			Description: ir.Description,
			Strict:      strictCompatible(ir.InputSchema),
			Parameters:  toOpenAISchema(ir.InputSchema),
		},
	}
}

// strictCompatible reports whether a schema can be expressed under OpenAI strict
// mode: no real oneofs and no open objects (map value schema or Struct) anywhere.
func strictCompatible(s *JSONSchema) bool {
	if s == nil {
		return true
	}
	if len(s.oneofMembers) > 0 || s.AdditionalProperties != nil {
		return false
	}
	if !strictCompatible(s.Items) {
		return false
	}
	for _, ps := range s.Properties {
		if !strictCompatible(ps) {
			return false
		}
	}
	return true
}

func toOpenAISchema(s *JSONSchema) *OpenAISchema {
	if s == nil {
		return nil
	}
	out := &OpenAISchema{Type: s.Type, Description: s.Description}
	for _, e := range s.Enum {
		out.Enum = append(out.Enum, e)
	}
	if s.Items != nil {
		out.Items = toOpenAISchema(s.Items)
	}
	if s.Type == "object" {
		// Strict mode: every declared property is required (an empty object still
		// emits `required: []`), so a property not marked ai_field.required is
		// made nullable: the model sends null, which protojson treats as unset,
		// instead of inventing a value for a server-populated or optional field.
		// A field buf.validate already requires (nonNullable) stays non-null, so
		// strict decoding still forces the model to supply it.
		// A proto map preserves its value schema as
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
				// A oneof member is optional (at most one of the group is set), so
				// listing it as required would make protojson reject every call.
				if !s.oneofMembers[name] {
					names = append(names, name)
					if !slices.Contains(s.Required, name) && !s.nonNullable[name] {
						nullable(out.Properties[name])
					}
				}
			}
			sort.Strings(names)
		}
		out.Required = &names
	}
	return out
}

// nullable widens a schema to also accept JSON null. An enum must list null
// too, or the enum constraint still rejects it.
func nullable(s *OpenAISchema) {
	s.Type = []string{s.Type.(string), "null"}
	if len(s.Enum) > 0 {
		s.Enum = append(s.Enum, nil)
	}
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
	// Gemini's function-declaration Schema (an OpenAPI subset) has no
	// additionalProperties and rejects the whole tools array on an unknown
	// field, so a map or Struct is described in prose instead.
	if note := openObjectNote(s.AdditionalProperties); note != "" {
		out.Description = strings.TrimSpace(out.Description + " " + note)
	}
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]*GeminiSchema, len(s.Properties))
		for name, ps := range s.Properties {
			out.Properties[name] = toGeminiSchema(ps)
		}
	}
	return out
}

// openObjectNote describes an open object's keys and values for a schema
// dialect that cannot express additionalProperties.
func openObjectNote(ap any) string {
	switch ap := ap.(type) {
	case *JSONSchema:
		note := "Object with arbitrary string keys; each value is a " + ap.Type
		if len(ap.Enum) > 0 {
			note += " (one of " + strings.Join(ap.Enum, ", ") + ")"
		}
		return note + "."
	case bool:
		if ap {
			return "Free-form JSON object."
		}
	}
	return ""
}

// Package mcp turns Gortexa's proto contract into AI-agent tools. It builds a
// provider-neutral intermediate representation (IR) from the ai.v1 annotations
// and proto descriptors, downgrades that IR to MCP / OpenAI / Gemini tool
// schemas, and serves a Streamable-HTTP MCP endpoint whose tools/call dispatches
// back through the full gRPC interceptor chain via an in-process loopback.
package mcp

import (
	"cmp"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	aiv1 "github.com/yshengliao/gortexa/gen/ai/v1"
)

const (
	maxToolNameLen = 64
	maxSchemaDepth = 8
)

// JSONSchema is the minimal, provider-portable schema subset Gortexa emits.
// It deliberately avoids oneOf/default/exclusiveMin/Max (rejected by OpenAI
// strict and Gemini) and renders every enum and int64 as a string.
type JSONSchema struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Properties  map[string]*JSONSchema `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Items       *JSONSchema            `json:"items,omitempty"`
	Enum        []string               `json:"enum,omitempty"`
}

// ToolIR is the provider-neutral description of one exposed RPC.
type ToolIR struct {
	Name        string
	Description string
	FullMethod  string // "/pkg.Service/Method"
	Input       protoreflect.MessageDescriptor
	Output      protoreflect.MessageDescriptor
	InputSchema *JSONSchema
	ReadOnly    bool
	Destructive bool
}

// ValidateTool enforces the ai.v1 tool constraints: a name no longer than 64
// chars and read_only/destructive being mutually exclusive.
func ValidateTool(name string, readOnly, destructive bool) error {
	if len(name) > maxToolNameLen {
		return fmt.Errorf("mcp: tool name %q exceeds %d chars", name, maxToolNameLen)
	}
	if readOnly && destructive {
		return fmt.Errorf("mcp: tool %q is both read_only and destructive", name)
	}
	return nil
}

func aiTool(m protoreflect.MethodDescriptor) *aiv1.AIToolOptions {
	opts := m.Options()
	if opts == nil || !proto.HasExtension(opts, aiv1.E_AiTool) {
		return nil
	}
	return proto.GetExtension(opts, aiv1.E_AiTool).(*aiv1.AIToolOptions)
}

func aiField(f protoreflect.FieldDescriptor) *aiv1.AIFieldOptions {
	opts := f.Options()
	if opts == nil || !proto.HasExtension(opts, aiv1.E_AiField) {
		return nil
	}
	return proto.GetExtension(opts, aiv1.E_AiField).(*aiv1.AIFieldOptions)
}

// BuildIR builds the tool IR for every exposed method of a service, validating
// the ai.v1 constraints (name length, read_only/destructive exclusivity).
func BuildIR(svc protoreflect.ServiceDescriptor) ([]ToolIR, error) {
	var tools []ToolIR
	methods := svc.Methods()
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		opt := aiTool(m)
		if opt == nil || !opt.GetExpose() {
			continue
		}
		name := opt.GetName()
		if name == "" {
			name = string(m.Name())
		}
		if err := ValidateTool(name, opt.GetReadOnly(), opt.GetDestructive()); err != nil {
			return nil, err
		}
		tools = append(tools, ToolIR{
			Name:        name,
			Description: opt.GetDescription(),
			FullMethod:  fmt.Sprintf("/%s/%s", svc.FullName(), m.Name()),
			Input:       m.Input(),
			Output:      m.Output(),
			InputSchema: schemaForMessage(m.Input(), 0),
			ReadOnly:    opt.GetReadOnly(),
			Destructive: opt.GetDestructive(),
		})
	}
	slices.SortFunc(tools, func(a, b ToolIR) int { return cmp.Compare(a.Name, b.Name) })
	return tools, nil
}

func schemaForMessage(md protoreflect.MessageDescriptor, depth int) *JSONSchema {
	s := &JSONSchema{Type: "object", Properties: map[string]*JSONSchema{}}
	if depth >= maxSchemaDepth {
		return s
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		fs := schemaForField(f, depth+1)
		if af := aiField(f); af != nil {
			if af.GetDescription() != "" {
				fs.Description = af.GetDescription()
			}
			if af.GetRequired() {
				s.Required = append(s.Required, f.JSONName())
			}
		}
		s.Properties[f.JSONName()] = fs
	}
	return s
}

func schemaForField(f protoreflect.FieldDescriptor, depth int) *JSONSchema {
	if f.IsMap() {
		return &JSONSchema{Type: "object"}
	}
	if f.IsList() {
		return &JSONSchema{Type: "array", Items: schemaForSingular(f, depth)}
	}
	return schemaForSingular(f, depth)
}

func schemaForSingular(f protoreflect.FieldDescriptor, depth int) *JSONSchema {
	switch f.Kind() {
	case protoreflect.BoolKind:
		return &JSONSchema{Type: "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
		protoreflect.Sfixed32Kind, protoreflect.Fixed32Kind:
		return &JSONSchema{Type: "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind,
		protoreflect.Sfixed64Kind, protoreflect.Fixed64Kind:
		// int64 family serializes as a JSON string in protojson (B-6 alignment).
		return &JSONSchema{Type: "string"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return &JSONSchema{Type: "number"}
	case protoreflect.StringKind, protoreflect.BytesKind:
		return &JSONSchema{Type: "string"}
	case protoreflect.EnumKind:
		return &JSONSchema{Type: "string", Enum: enumValues(f.Enum())}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch f.Message().FullName() {
		case "google.protobuf.Timestamp", "google.protobuf.Duration":
			return &JSONSchema{Type: "string"}
		default:
			return schemaForMessage(f.Message(), depth)
		}
	default:
		return &JSONSchema{Type: "string"}
	}
}

func enumValues(ed protoreflect.EnumDescriptor) []string {
	vals := ed.Values()
	out := make([]string, 0, vals.Len())
	for i := 0; i < vals.Len(); i++ {
		out = append(out, string(vals.Get(i).Name()))
	}
	return out
}

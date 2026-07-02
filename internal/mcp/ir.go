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
	// AdditionalProperties is either a *JSONSchema (the value schema of a proto
	// map, so arbitrary keys are allowed with a typed value) or the bool `true`
	// (a google.protobuf.Struct, i.e. a free-form object). It is nil for closed
	// messages; the OpenAI strict downgrade renders that nil as `false`.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
	// oneofMembers is the set of property JSONNames that belong to a real proto
	// oneof (only one may be set at a time). The OpenAI-strict downgrade excludes
	// them from `required` so it never emits a schema protojson always rejects.
	// Not serialized.
	oneofMembers map[string]bool
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
		inputSchema, err := schemaForMessage(m.Input(), 0)
		if err != nil {
			return nil, fmt.Errorf("mcp: tool %q input schema: %w", name, err)
		}
		tools = append(tools, ToolIR{
			Name:        name,
			Description: opt.GetDescription(),
			FullMethod:  fmt.Sprintf("/%s/%s", svc.FullName(), m.Name()),
			Input:       m.Input(),
			Output:      m.Output(),
			InputSchema: inputSchema,
			ReadOnly:    opt.GetReadOnly(),
			Destructive: opt.GetDestructive(),
		})
	}
	slices.SortFunc(tools, func(a, b ToolIR) int { return cmp.Compare(a.Name, b.Name) })
	return tools, nil
}

func schemaForMessage(md protoreflect.MessageDescriptor, depth int) (*JSONSchema, error) {
	// Fail loud rather than silently emitting an empty object: a schema this deep
	// (or a self-referential message) signals a contract mismatch the caller
	// should see, not paper over.
	if depth >= maxSchemaDepth {
		return nil, fmt.Errorf("schema for message %q exceeds maximum depth %d", md.FullName(), maxSchemaDepth)
	}
	s := &JSONSchema{Type: "object", Properties: map[string]*JSONSchema{}}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		fs, err := schemaForField(f, depth+1)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.FullName(), err)
		}
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
	// Record members of real (non-synthetic) oneofs. proto3 `optional` fields
	// each get a synthetic single-field oneof, which we ignore.
	oneofs := md.Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		o := oneofs.Get(i)
		if o.IsSynthetic() {
			continue
		}
		of := o.Fields()
		for j := 0; j < of.Len(); j++ {
			if s.oneofMembers == nil {
				s.oneofMembers = map[string]bool{}
			}
			s.oneofMembers[string(of.Get(j).JSONName())] = true
		}
	}
	return s, nil
}

func schemaForField(f protoreflect.FieldDescriptor, depth int) (*JSONSchema, error) {
	if f.IsMap() {
		// A proto map<K,V> serializes as a JSON object keyed by string; expose the
		// value shape via additionalProperties so arbitrary keys stay typed.
		val, err := schemaForSingular(f.MapValue(), depth)
		if err != nil {
			return nil, err
		}
		return &JSONSchema{Type: "object", AdditionalProperties: val}, nil
	}
	if f.IsList() {
		items, err := schemaForSingular(f, depth)
		if err != nil {
			return nil, err
		}
		return &JSONSchema{Type: "array", Items: items}, nil
	}
	return schemaForSingular(f, depth)
}

func schemaForSingular(f protoreflect.FieldDescriptor, depth int) (*JSONSchema, error) {
	switch f.Kind() {
	case protoreflect.BoolKind:
		return &JSONSchema{Type: "boolean"}, nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Uint32Kind,
		protoreflect.Sfixed32Kind, protoreflect.Fixed32Kind:
		return &JSONSchema{Type: "integer"}, nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Uint64Kind,
		protoreflect.Sfixed64Kind, protoreflect.Fixed64Kind:
		// int64 family serializes as a JSON string in protojson (B-6 alignment).
		return &JSONSchema{Type: "string"}, nil
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return &JSONSchema{Type: "number"}, nil
	case protoreflect.StringKind, protoreflect.BytesKind:
		return &JSONSchema{Type: "string"}, nil
	case protoreflect.EnumKind:
		return &JSONSchema{Type: "string", Enum: enumValues(f.Enum())}, nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return schemaForWellKnownOrMessage(f.Message(), depth)
	default:
		return &JSONSchema{Type: "string"}, nil
	}
}

// schemaForWellKnownOrMessage maps protobuf well-known types to their protojson
// JSON form (instead of recursing into their internal fields, which would emit a
// schema that doesn't match the wire format). Types the portable schema subset
// cannot safely express (Value, Any) are rejected so BuildIR fails loud.
func schemaForWellKnownOrMessage(md protoreflect.MessageDescriptor, depth int) (*JSONSchema, error) {
	switch md.FullName() {
	case "google.protobuf.Timestamp", "google.protobuf.Duration":
		return &JSONSchema{Type: "string"}, nil
	case "google.protobuf.BoolValue":
		return &JSONSchema{Type: "boolean"}, nil
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		return &JSONSchema{Type: "integer"}, nil
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		// int64 wrapper values serialize as JSON strings in protojson.
		return &JSONSchema{Type: "string"}, nil
	case "google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return &JSONSchema{Type: "number"}, nil
	case "google.protobuf.StringValue", "google.protobuf.BytesValue":
		return &JSONSchema{Type: "string"}, nil
	case "google.protobuf.FieldMask":
		return &JSONSchema{
			Type:        "string",
			Description: "Comma-separated protobuf field paths, e.g. foo.bar,baz.",
		}, nil
	case "google.protobuf.Struct":
		return &JSONSchema{Type: "object", AdditionalProperties: true}, nil
	case "google.protobuf.Value":
		return nil, fmt.Errorf("google.protobuf.Value is not supported in exposed MCP tool schemas: the portable schema subset cannot express arbitrary JSON values")
	case "google.protobuf.Any":
		return nil, fmt.Errorf("google.protobuf.Any is not supported in exposed MCP tool schemas: design an explicit typed request instead")
	default:
		return schemaForMessage(md, depth)
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

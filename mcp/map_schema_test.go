package mcp

import (
	"testing"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestSchemaForMapFields(t *testing.T) {
	md := mapTestMessage(t)
	tests := []struct {
		field string
		want  *JSONSchema
	}{
		{field: "labels", want: &JSONSchema{Type: "string"}},
		{field: "counts", want: &JSONSchema{Type: "string"}},
		{field: "statuses", want: &JSONSchema{Type: "string", Enum: []string{"STATUS_UNSPECIFIED", "STATUS_ACTIVE"}}},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, err := schemaForField(md.Fields().ByName(protoreflect.Name(tt.field)), 1)
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != "object" {
				t.Fatalf("schema type = %q, want object", got.Type)
			}
			ap, ok := got.AdditionalProperties.(*JSONSchema)
			if !ok || ap == nil {
				t.Fatalf("map schema must allow arbitrary keys via additionalProperties, got %#v", got.AdditionalProperties)
			}
			if ap.Type != tt.want.Type {
				t.Fatalf("additionalProperties type = %q, want %q", ap.Type, tt.want.Type)
			}
			if len(tt.want.Enum) > 0 {
				if len(ap.Enum) != len(tt.want.Enum) {
					t.Fatalf("enum = %v, want %v", ap.Enum, tt.want.Enum)
				}
				for i := range tt.want.Enum {
					if ap.Enum[i] != tt.want.Enum[i] {
						t.Fatalf("enum = %v, want %v", ap.Enum, tt.want.Enum)
					}
				}
			}
		})
	}
}

func TestDowngradeOpenAIPreservesMapAdditionalProperties(t *testing.T) {
	sch, err := schemaForMessage(mapTestMessage(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	ir := ToolIR{Name: "map_tool", InputSchema: sch}
	fn := DowngradeOpenAI(ir).Function
	// A map field is an open object, which OpenAI strict mode forbids; the tool
	// must be emitted non-strict rather than as a schema the API rejects.
	if fn.Strict {
		t.Error("map-bearing tool should be non-strict")
	}
	params := fn.Parameters
	if params == nil {
		t.Fatal("parameters nil")
	}
	if got, ok := params.AdditionalProperties.(bool); !ok || got {
		t.Fatalf("top-level additionalProperties = %#v, want false", params.AdditionalProperties)
	}

	tests := map[string]string{"labels": "string", "counts": "string", "statuses": "string"}
	for name, typ := range tests {
		prop := params.Properties[name]
		if prop == nil {
			t.Fatalf("missing property %q", name)
		}
		ap, ok := prop.AdditionalProperties.(*OpenAISchema)
		if !ok {
			t.Fatalf("%s additionalProperties = %#v, want schema", name, prop.AdditionalProperties)
		}
		if ap.Type != typ {
			t.Fatalf("%s additionalProperties type = %q, want %q", name, ap.Type, typ)
		}
	}
	statuses := params.Properties["statuses"].AdditionalProperties.(*OpenAISchema)
	if len(statuses.Enum) != 2 || statuses.Enum[0] != "STATUS_UNSPECIFIED" || statuses.Enum[1] != "STATUS_ACTIVE" {
		t.Fatalf("statuses enum = %v", statuses.Enum)
	}
}

// Gemini downgrade must also carry a proto map's value schema via
// additionalProperties (not silently flatten it to a closed object).
func TestDowngradeGeminiPreservesMapAdditionalProperties(t *testing.T) {
	sch, err := schemaForMessage(mapTestMessage(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	params := DowngradeGemini(ToolIR{Name: "map_tool", InputSchema: sch}).Parameters
	if params == nil {
		t.Fatal("parameters nil")
	}
	for _, name := range []string{"labels", "counts", "statuses"} {
		prop := params.Properties[name]
		if prop == nil {
			t.Fatalf("missing property %q", name)
		}
		ap, ok := prop.AdditionalProperties.(*GeminiSchema)
		if !ok || ap == nil {
			t.Fatalf("%s additionalProperties = %#v, want *GeminiSchema", name, prop.AdditionalProperties)
		}
		if ap.Type != "string" {
			t.Fatalf("%s map value type = %q, want string", name, ap.Type)
		}
	}
}

func mapTestMessage(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Syntax:  new("proto3"),
		Name:    new("test/map.proto"),
		Package: new("test"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: new("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: new("STATUS_UNSPECIFIED"), Number: new(int32(0))},
				{Name: new("STATUS_ACTIVE"), Number: new(int32(1))},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("MapInput"),
			Field: []*descriptorpb.FieldDescriptorProto{
				mapField("labels", 1, ".test.MapInput.LabelsEntry"),
				mapField("counts", 2, ".test.MapInput.CountsEntry"),
				mapField("statuses", 3, ".test.MapInput.StatusesEntry"),
			},
			NestedType: []*descriptorpb.DescriptorProto{
				mapEntry("LabelsEntry", descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
				mapEntry("CountsEntry", descriptorpb.FieldDescriptorProto_TYPE_INT64, ""),
				mapEntry("StatusesEntry", descriptorpb.FieldDescriptorProto_TYPE_ENUM, ".test.Status"),
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return file.Messages().ByName("MapInput")
}

func mapField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{Name: new(name), Number: new(number), Label: new(descriptorpb.FieldDescriptorProto_LABEL_REPEATED), Type: new(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE), TypeName: new(typeName), JsonName: new(name)}
}

func mapEntry(name string, valueType descriptorpb.FieldDescriptorProto_Type, valueTypeName string) *descriptorpb.DescriptorProto {
	value := &descriptorpb.FieldDescriptorProto{Name: new("value"), Number: new(int32(2)), Label: new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL), Type: new(valueType)}
	if valueTypeName != "" {
		value.TypeName = new(valueTypeName)
	}
	return &descriptorpb.DescriptorProto{
		Name: new(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: new("key"), Number: new(int32(1)), Label: new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL), Type: new(descriptorpb.FieldDescriptorProto_TYPE_STRING)},
			value,
		},
		Options: &descriptorpb.MessageOptions{MapEntry: new(true)},
	}
}

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
	params := DowngradeOpenAI(ir).Function.Parameters
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

func mapTestMessage(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Syntax:  str("proto3"),
		Name:    str("test/map.proto"),
		Package: str("test"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: str("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: str("STATUS_UNSPECIFIED"), Number: i32(0)},
				{Name: str("STATUS_ACTIVE"), Number: i32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: str("MapInput"),
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
	return &descriptorpb.FieldDescriptorProto{Name: str(name), Number: i32(number), Label: label(descriptorpb.FieldDescriptorProto_LABEL_REPEATED), Type: fieldType(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE), TypeName: str(typeName), JsonName: str(name)}
}

func mapEntry(name string, valueType descriptorpb.FieldDescriptorProto_Type, valueTypeName string) *descriptorpb.DescriptorProto {
	value := &descriptorpb.FieldDescriptorProto{Name: str("value"), Number: i32(2), Label: label(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL), Type: fieldType(valueType)}
	if valueTypeName != "" {
		value.TypeName = str(valueTypeName)
	}
	return &descriptorpb.DescriptorProto{
		Name: str(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: str("key"), Number: i32(1), Label: label(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL), Type: fieldType(descriptorpb.FieldDescriptorProto_TYPE_STRING)},
			value,
		},
		Options: &descriptorpb.MessageOptions{MapEntry: boolp(true)},
	}
}

func str(s string) *string { return &s }
func i32(i int32) *int32   { return &i }
func boolp(b bool) *bool   { return &b }
func label(l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto_Label {
	return &l
}
func fieldType(ft descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto_Type {
	return &ft
}

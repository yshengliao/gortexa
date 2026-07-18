package mcp

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	aiv1 "github.com/yshengliao/gortexa/gen/gortexa/ai/v1"
)

// oneofToolService builds a one-method service whose input message carries a
// real (non-synthetic) proto oneof "choice" with members a/b plus a plain id
// field, exercising ir.go oneofMembers population and downgrade.go's
// strictCompatible/oneof-required handling.
func oneofToolService(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()

	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, aiv1.E_AiTool, &aiv1.AIToolOptions{Expose: true, Name: "oneof_tool"})

	strField := func(name string, number int32, oneof *int32) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{
			Name:     str(name),
			JsonName: str(name),
			Number:   i32(number),
			Label:    label(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			Type:     fieldType(descriptorpb.FieldDescriptorProto_TYPE_STRING),
		}
		f.OneofIndex = oneof
		return f
	}

	fd := &descriptorpb.FileDescriptorProto{
		Syntax:  str("proto3"),
		Name:    str("mcp/oneoftest.proto"),
		Package: str("mcp.oneoftest"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: str("Request"),
				Field: []*descriptorpb.FieldDescriptorProto{
					strField("id", 1, nil),
					strField("a", 2, i32(0)),
					strField("b", 3, i32(0)),
				},
				OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: str("choice")}},
			},
			{Name: str("Response")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: str("Tools"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       str("Call"),
				InputType:  str(".mcp.oneoftest.Request"),
				OutputType: str(".mcp.oneoftest.Response"),
				Options:    methodOpts,
			}},
		}},
	}
	file, err := protodesc.NewFile(fd, nil)
	if err != nil {
		t.Fatal(err)
	}
	return file.Services().ByName("Tools")
}

func TestBuildIRPopulatesOneofMembers(t *testing.T) {
	tools, err := BuildIR(oneofToolService(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	sch := tools[0].InputSchema
	if sch.oneofMembers["a"] != true || sch.oneofMembers["b"] != true {
		t.Fatalf("oneof members a/b not recorded: %#v", sch.oneofMembers)
	}
	// A plain field outside the oneof must not be recorded as a member.
	if sch.oneofMembers["id"] {
		t.Fatalf("non-oneof field id wrongly recorded as oneof member: %#v", sch.oneofMembers)
	}
}

func TestStrictCompatibleRejectsOneof(t *testing.T) {
	tools, err := BuildIR(oneofToolService(t))
	if err != nil {
		t.Fatal(err)
	}
	sch := tools[0].InputSchema

	// A schema carrying a real oneof cannot satisfy OpenAI strict mode.
	if strictCompatible(sch) {
		t.Fatal("schema with a real oneof must be strict-incompatible")
	}

	fn := DowngradeOpenAI(tools[0]).Function
	if fn.Strict {
		t.Fatal("oneof-bearing tool must be emitted non-strict")
	}
	if fn.Parameters.Required == nil {
		t.Fatal("object schema must emit required")
	}
	// Oneof members are optional (at most one is set), so they must be excluded
	// from required; the plain id field stays required-eligible list membership.
	req := map[string]bool{}
	for _, r := range *fn.Parameters.Required {
		req[r] = true
	}
	if req["a"] || req["b"] {
		t.Fatalf("oneof members must not be required: %v", *fn.Parameters.Required)
	}
	if !req["id"] {
		t.Fatalf("non-oneof field id should be in required: %v", *fn.Parameters.Required)
	}
}

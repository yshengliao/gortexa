package mcp_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	aiv1 "github.com/yshengliao/gortexa/gen/ai/v1"
	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/mcp"
)

func TestBuildIRExposesAnnotatedMethods(t *testing.T) {
	svc := resourcev1.File_resource_v1_resource_proto.Services().Get(0)
	tools, err := mcp.BuildIR(svc)
	if err != nil {
		t.Fatal(err)
	}
	// create, get, list, delete are exposed; update is not.
	want := map[string]struct {
		readOnly    bool
		destructive bool
	}{
		"create_resource": {false, true},
		"get_resource":    {true, false},
		"list_resources":  {true, false},
		"delete_resource": {false, true},
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		w, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if tool.ReadOnly != w.readOnly || tool.Destructive != w.destructive {
			t.Errorf("%s: readOnly=%v destructive=%v, want %v/%v", tool.Name, tool.ReadOnly, tool.Destructive, w.readOnly, w.destructive)
		}
		if tool.FullMethod == "" || tool.InputSchema == nil {
			t.Errorf("%s: incomplete IR", tool.Name)
		}
	}
}

func TestBuildIRSchemaAndRequired(t *testing.T) {
	svc := resourcev1.File_resource_v1_resource_proto.Services().Get(0)
	tools, _ := mcp.BuildIR(svc)
	for _, tool := range tools {
		if tool.Name != "get_resource" {
			continue
		}
		idSchema, ok := tool.InputSchema.Properties["id"]
		if !ok || idSchema.Type != "string" {
			t.Fatalf("get_resource id schema = %+v", idSchema)
		}
		found := false
		for _, r := range tool.InputSchema.Required {
			if r == "id" {
				found = true
			}
		}
		if !found {
			t.Fatalf("id should be required (ai_field.required), got %v", tool.InputSchema.Required)
		}
	}
}

func TestValidateTool(t *testing.T) {
	if err := mcp.ValidateTool("ok", true, false); err != nil {
		t.Errorf("read-only tool should be valid: %v", err)
	}
	if err := mcp.ValidateTool("bad", true, true); err == nil {
		t.Error("read_only ∧ destructive must error")
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if err := mcp.ValidateTool(string(long), false, false); err == nil {
		t.Error("name > 64 chars must error")
	}
}

func TestBuildIRRejectsRecursiveInputSchema(t *testing.T) {
	svc := depthLimitService(t, "Recursive", []messageSpec{
		{name: "Recursive", fieldType: ".mcp.depthtest.Recursive"},
	})

	_, err := mcp.BuildIR(svc)
	if err == nil {
		t.Fatal("BuildIR should reject recursive schemas that exceed the maximum depth")
	}
	if !strings.Contains(err.Error(), "exceeds maximum depth") {
		t.Fatalf("BuildIR error = %v, want maximum depth error", err)
	}
}

func TestBuildIRRejectsDeeplyNestedInputSchema(t *testing.T) {
	messages := []messageSpec{
		{name: "Level0", fieldType: ".mcp.depthtest.Level1"},
		{name: "Level1", fieldType: ".mcp.depthtest.Level2"},
		{name: "Level2", fieldType: ".mcp.depthtest.Level3"},
		{name: "Level3", fieldType: ".mcp.depthtest.Level4"},
		{name: "Level4", fieldType: ".mcp.depthtest.Level5"},
		{name: "Level5", fieldType: ".mcp.depthtest.Level6"},
		{name: "Level6", fieldType: ".mcp.depthtest.Level7"},
		{name: "Level7", fieldType: ".mcp.depthtest.Level8"},
		{name: "Level8"},
	}
	svc := depthLimitService(t, "Level0", messages)

	_, err := mcp.BuildIR(svc)
	if err == nil {
		t.Fatal("BuildIR should reject schemas that exceed the maximum depth")
	}
	if !strings.Contains(err.Error(), "exceeds maximum depth") {
		t.Fatalf("BuildIR error = %v, want maximum depth error", err)
	}
}

type messageSpec struct {
	name      string
	fieldType string
}

func depthLimitService(t *testing.T, input string, messages []messageSpec) protoreflect.ServiceDescriptor {
	t.Helper()

	msgProtos := make([]*descriptorpb.DescriptorProto, 0, len(messages)+1)
	for _, msg := range messages {
		fields := []*descriptorpb.FieldDescriptorProto(nil)
		if msg.fieldType != "" {
			fields = []*descriptorpb.FieldDescriptorProto{{
				Name:     new("child"),
				JsonName: new("child"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: new(msg.fieldType),
			}}
		}
		msgProtos = append(msgProtos, &descriptorpb.DescriptorProto{
			Name:  new(msg.name),
			Field: fields,
		})
	}
	msgProtos = append(msgProtos, &descriptorpb.DescriptorProto{Name: new("Empty")})

	methodOptions := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOptions, aiv1.E_AiTool, &aiv1.AIToolOptions{
		Expose: true,
		Name:   "depth_test",
	})

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        new("mcp/depthtest.proto"),
		Package:     new("mcp.depthtest"),
		Syntax:      new("proto3"),
		MessageType: msgProtos,
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("DepthService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       new("Depth"),
				InputType:  new(".mcp.depthtest." + input),
				OutputType: new(".mcp.depthtest.Empty"),
				Options:    methodOptions,
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("build test descriptor: %v", err)
	}
	return fd.Services().Get(0)
}

func TestBuildIRRejectsStreamingTool(t *testing.T) {
	methodOptions := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOptions, aiv1.E_AiTool, &aiv1.AIToolOptions{
		Expose: true,
		Name:   "stream_test",
	})

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        new("mcp/streamtest.proto"),
		Package:     new("mcp.streamtest"),
		Syntax:      new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: new("Empty")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("StreamService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:            new("Watch"),
				InputType:       new(".mcp.streamtest.Empty"),
				OutputType:      new(".mcp.streamtest.Empty"),
				ServerStreaming: new(true),
				Options:         methodOptions,
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("build test descriptor: %v", err)
	}

	_, err = mcp.BuildIR(fd.Services().Get(0))
	if err == nil {
		t.Fatal("BuildIR should reject an exposed streaming RPC at build time, not at tools/call")
	}
	if !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("BuildIR error = %v, want streaming rejection", err)
	}
}

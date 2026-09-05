package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/anypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	_ "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"

	aiv1 "github.com/yshengliao/gortexa/api/gen/gortexa/ai/v1"
	"github.com/yshengliao/gortexa/testutil"
)

func TestSchemaForWellKnownTypesGolden(t *testing.T) {
	md := testMessageDescriptor(t, "wkt_supported.proto", []string{
		"google/protobuf/wrappers.proto",
		"google/protobuf/field_mask.proto",
		"google/protobuf/struct.proto",
	}, []*descriptorpb.FieldDescriptorProto{
		messageField("bool_wrapper", 1, ".google.protobuf.BoolValue"),
		messageField("int32_wrapper", 2, ".google.protobuf.Int32Value"),
		messageField("int64_wrapper", 3, ".google.protobuf.Int64Value"),
		messageField("double_wrapper", 4, ".google.protobuf.DoubleValue"),
		messageField("string_wrapper", 5, ".google.protobuf.StringValue"),
		messageField("bytes_wrapper", 6, ".google.protobuf.BytesValue"),
		messageField("field_mask", 7, ".google.protobuf.FieldMask"),
		messageField("struct_value", 8, ".google.protobuf.Struct"),
	})

	schema, err := schemaForMessage(md, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "mcp_well_known_schema", b)
}

func TestBuildIRRejectsUnsupportedWellKnownTypes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		imported string
		typeName string
		wantErr  string
	}{
		{name: "value", imported: "google/protobuf/struct.proto", typeName: ".google.protobuf.Value", wantErr: "google.protobuf.Value is not supported"},
		{name: "any", imported: "google/protobuf/any.proto", typeName: ".google.protobuf.Any", wantErr: "google.protobuf.Any is not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := testServiceDescriptor(t, tc.name+".proto", []string{tc.imported}, []*descriptorpb.FieldDescriptorProto{
				messageField(tc.name, 1, tc.typeName),
			})
			_, err := BuildIR(svc)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("BuildIR error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func testMessageDescriptor(t *testing.T, path string, deps []string, fields []*descriptorpb.FieldDescriptorProto) protoreflect.MessageDescriptor {
	t.Helper()
	file := testFileDescriptor(t, path, deps, fields, false)
	return file.Messages().ByName("Request")
}

func testServiceDescriptor(t *testing.T, path string, deps []string, fields []*descriptorpb.FieldDescriptorProto) protoreflect.ServiceDescriptor {
	t.Helper()
	file := testFileDescriptor(t, path, deps, fields, true)
	return file.Services().ByName("Tools")
}

func testFileDescriptor(t *testing.T, path string, deps []string, fields []*descriptorpb.FieldDescriptorProto, expose bool) protoreflect.FileDescriptor {
	t.Helper()
	methodOpts := &descriptorpb.MethodOptions{}
	if expose {
		proto.SetExtension(methodOpts, aiv1.E_AiTool, &aiv1.AIToolOptions{Expose: true, Name: "wkt_tool"})
	}
	fd := &descriptorpb.FileDescriptorProto{
		Syntax:     new("proto3"),
		Name:       new(path),
		Package:    new("mcp.wkt.test"),
		Dependency: deps,
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("Request"), Field: fields},
			{Name: new("Response")},
		},
	}
	if expose {
		fd.Service = []*descriptorpb.ServiceDescriptorProto{{
			Name: new("Tools"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       new("Call"),
				InputType:  new(".mcp.wkt.test.Request"),
				OutputType: new(".mcp.wkt.test.Response"),
				Options:    methodOpts,
			}},
		}}
	}
	file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func messageField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     new(name),
		JsonName: new(camelJSONName(name)),
		Number:   new(number),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		TypeName: new(typeName),
	}
}

func camelJSONName(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

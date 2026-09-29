package mcp

import (
	"testing"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestValidateRequires covers the branches the sample proto does not exercise:
// a proto3 `optional` string with min_len (validated only when set, so it stays
// nullable) and IGNORE_IF_ZERO_VALUE (unset skips the rule).
func TestValidateRequires(t *testing.T) {
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	withRules := func(r *validate.FieldRules) *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, validate.E_Field, r)
		return o
	}
	minLen := func(n uint64) *validate.FieldRules {
		return validate.FieldRules_builder{String: validate.StringRules_builder{MinLen: proto.Uint64(n)}.Build()}.Build()
	}
	ignored := minLen(1)
	ignored.SetIgnore(validate.Ignore_IGNORE_IF_ZERO_VALUE)
	field := func(name string, num int32, o *descriptorpb.FieldOptions, oneof *int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), JsonName: proto.String(name), Number: proto.Int32(num),
			Type: &str, Label: &opt, Options: o, OneofIndex: oneof,
			Proto3Optional: func() *bool {
				if oneof != nil {
					return proto.Bool(true)
				}
				return nil
			}(),
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("mcp_validate_required_test.proto"),
		Package:    proto.String("mcptest"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"buf/validate/validate.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("M"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("plain", 1, nil, nil),
				field("min_len", 2, withRules(minLen(1)), nil),
				field("min_len_zero", 3, withRules(minLen(0)), nil),
				field("optional_min_len", 4, withRules(minLen(1)), proto.Int32(0)),
				field("ignore_zero", 5, withRules(ignored), nil),
				field("required", 6, withRules(validate.FieldRules_builder{Required: proto.Bool(true)}.Build()), nil),
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_optional_min_len")}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	fields := fd.Messages().Get(0).Fields()
	want := map[string]bool{
		"plain": false, "min_len": true, "min_len_zero": false,
		"optional_min_len": false, "ignore_zero": false, "required": true,
	}
	for name, w := range want {
		f := fields.ByName(protoreflect.Name(name))
		if f == nil {
			t.Fatalf("field %q missing", name)
		}
		if got := validateRequires(f); got != w {
			t.Errorf("validateRequires(%s) = %v, want %v", name, got, w)
		}
	}
}

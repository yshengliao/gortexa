package mcp_test

import (
	"testing"

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

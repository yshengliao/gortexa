package mcp_test

import (
	"strings"
	"testing"

	resourcev1 "github.com/yshengliao/gortexa/gen/resource/v1"
	"github.com/yshengliao/gortexa/internal/mcp"
	"github.com/yshengliao/gortexa/testutil"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestExportSchemasGolden(t *testing.T) {
	svcs := []protoreflect.ServiceDescriptor{resourcev1.File_resource_v1_resource_proto.Services().Get(0)}
	for _, format := range []string{"mcp", "openai", "gemini"} {
		out, err := mcp.ExportSchemas(format, svcs)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		testutil.Golden(t, "export_"+format, out)
	}
}

func TestExportSchemasUnknownFormat(t *testing.T) {
	_, err := mcp.ExportSchemas("yaml", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown export format") {
		t.Fatalf("want unknown-format error, got %v", err)
	}
}

package cli

import (
	"fmt"
	"os"
	"strings"
)

// wireServer inserts the import, gRPC registration, gateway registration and MCP
// service entry for a new service at the `// gortexa:*` markers in main.go. It is
// idempotent: a line that already exists is left as-is.
func wireServer(mainPath string, d tmplData) error {
	b, err := os.ReadFile(mainPath)
	if err != nil {
		return err
	}
	src := string(b)

	importLine := fmt.Sprintf("\t%s %q", d.GoPkg, d.Module+"/gen/"+d.Domain+"/"+d.Version)
	registerLine := fmt.Sprintf("\t%s.Register%sServiceServer(app.GRPCServer(), logic.New%sService())", d.GoPkg, d.Entity, d.Entity)
	gatewayBlock := fmt.Sprintf("\tif err := %s.Register%sServiceHandler(ctx, gateway, conn); err != nil {\n\t\treturn fmt.Errorf(\"register %s gateway: %%w\", err)\n\t}", d.GoPkg, d.Entity, d.Snake)
	mcpLine := fmt.Sprintf("\t\t%q,", d.Domain+"."+d.Version+"."+d.Entity+"Service")

	for _, ins := range []struct{ marker, line string }{
		{"// gortexa:import", importLine},
		{"// gortexa:register", registerLine},
		{"// gortexa:gateway", gatewayBlock},
		{"// gortexa:mcp", mcpLine},
	} {
		if src, err = insertBeforeMarker(src, ins.marker, ins.line); err != nil {
			return err
		}
	}
	return os.WriteFile(mainPath, []byte(src), 0o644)
}

// insertBeforeMarker inserts line on its own line directly above the line that
// contains marker. If line is already present anywhere, src is returned unchanged.
func insertBeforeMarker(src, marker, line string) (string, error) {
	if strings.Contains(src, line) {
		return src, nil
	}
	idx := strings.Index(src, marker)
	if idx < 0 {
		return "", fmt.Errorf("marker %q not found — is this a gortexa project's cmd/server/main.go?", marker)
	}
	lineStart := strings.LastIndex(src[:idx], "\n") + 1
	return src[:lineStart] + line + "\n" + src[lineStart:], nil
}

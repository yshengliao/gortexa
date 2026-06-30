package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureMain is a minimal cmd/server/main.go carrying the gortexa:* markers. It
// is used only for text-level wiring assertions (it is not compiled).
const fixtureMain = `package main

import (
	resourcev1 "example.com/demo/gen/resource/v1"
	// gortexa:import — marker
	"example.com/demo/internal/logic"
)

func run() error {
	resourcev1.RegisterResourceServiceServer(app.GRPCServer(), logic.NewResourceService())
	// gortexa:register — marker
	_ = 0

	if err := resourcev1.RegisterResourceServiceHandler(ctx, gateway, conn); err != nil {
		return err
	}
	// gortexa:gateway — marker
	app.SetGateway(gateway)

	descs, _ := mcp.ServiceDescriptors(
		"resource.v1.ResourceService",
		// gortexa:mcp — marker
	)
	_ = descs
	return nil
}
`

func setupFixtureProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.26.0\n")
	writeFixture(t, filepath.Join(root, "cmd", "server", "main.go"), fixtureMain)
	return root
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGenerateAPIWritesAndWires(t *testing.T) {
	root := setupFixtureProject(t)
	data, err := parseTarget("billing/v1", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	data.Module = "example.com/demo"
	if err := generateAPI(root, data, genOpts{skipGen: true}); err != nil {
		t.Fatal(err)
	}

	proto := readFile(t, filepath.Join(root, "proto", "billing", "v1", "invoice.proto"))
	for _, want := range []string{"package billing.v1;", "service InvoiceService", `name: "create_invoice"`, "/v1/invoices/{invoice.id}"} {
		if !strings.Contains(proto, want) {
			t.Errorf("proto missing %q", want)
		}
	}

	logic := readFile(t, filepath.Join(root, "internal", "logic", "invoice.go"))
	for _, want := range []string{
		"func NewInvoiceService()",
		"func (s *InvoiceService) CreateInvoice",
		`billingv1 "example.com/demo/gen/billing/v1"`,
	} {
		if !strings.Contains(logic, want) {
			t.Errorf("logic missing %q", want)
		}
	}

	main := readFile(t, filepath.Join(root, "cmd", "server", "main.go"))
	for _, want := range []string{
		`billingv1 "example.com/demo/gen/billing/v1"`,
		"billingv1.RegisterInvoiceServiceServer(app.GRPCServer(), logic.NewInvoiceService())",
		"billingv1.RegisterInvoiceServiceHandler(ctx, gateway, conn)",
		`"billing.v1.InvoiceService",`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go missing wiring %q", want)
		}
	}
}

func TestWireServerIdempotent(t *testing.T) {
	root := setupFixtureProject(t)
	mainPath := filepath.Join(root, "cmd", "server", "main.go")
	data, _ := parseTarget("billing/v1", "Invoice")
	data.Module = "example.com/demo"
	if err := wireServer(mainPath, data); err != nil {
		t.Fatal(err)
	}
	if err := wireServer(mainPath, data); err != nil {
		t.Fatal(err)
	}
	main := readFile(t, mainPath)
	if n := strings.Count(main, "billingv1.RegisterInvoiceServiceServer"); n != 1 {
		t.Errorf("register line appears %d times, want 1", n)
	}
	if n := strings.Count(main, `"billing.v1.InvoiceService",`); n != 1 {
		t.Errorf("mcp entry appears %d times, want 1", n)
	}
}

func TestGenerateAPIExistsGuard(t *testing.T) {
	root := setupFixtureProject(t)
	data, _ := parseTarget("billing/v1", "Invoice")
	data.Module = "example.com/demo"
	if err := generateAPI(root, data, genOpts{skipGen: true, noWire: true}); err != nil {
		t.Fatal(err)
	}
	if err := generateAPI(root, data, genOpts{skipGen: true, noWire: true}); err == nil {
		t.Error("expected error when proto already exists without --force")
	}
}

func TestWireServerMissingMarker(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.go")
	writeFixture(t, mainPath, "package main\nfunc main() {}\n")
	data, _ := parseTarget("billing/v1", "Invoice")
	data.Module = "example.com/demo"
	if err := wireServer(mainPath, data); err == nil {
		t.Error("expected error when markers are absent")
	}
}

func TestGenerateAPIPreflightsExistingFilesBeforeWriting(t *testing.T) {
	root := setupFixtureProject(t)
	data, _ := parseTarget("billing/v1", "Invoice")
	data.Module = "example.com/demo"
	logicPath := filepath.Join(root, "internal", "logic", "invoice.go")
	writeFixture(t, logicPath, "keep me")

	err := generateAPI(root, data, genOpts{skipGen: true, noWire: true})
	if err == nil {
		t.Fatal("expected existing logic file to reject generation")
	}
	if _, statErr := os.Stat(filepath.Join(root, "proto", "billing", "v1", "invoice.proto")); !os.IsNotExist(statErr) {
		t.Fatalf("proto was written before existing-file failure: %v", statErr)
	}
	if got := readFile(t, logicPath); got != "keep me" {
		t.Fatalf("logic file changed to %q", got)
	}
}

func TestGenerateAPIPreflightsWireMarkersBeforeWriting(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.26.0\n")
	writeFixture(t, filepath.Join(root, "cmd", "server", "main.go"), "package main\nfunc main() {}\n")
	data, _ := parseTarget("billing/v1", "Invoice")
	data.Module = "example.com/demo"

	err := generateAPI(root, data, genOpts{skipGen: true})
	if err == nil {
		t.Fatal("expected missing markers to reject generation")
	}
	if _, statErr := os.Stat(filepath.Join(root, "proto", "billing", "v1", "invoice.proto")); !os.IsNotExist(statErr) {
		t.Fatalf("proto was written before wire-marker failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "internal", "logic", "invoice.go")); !os.IsNotExist(statErr) {
		t.Fatalf("logic was written before wire-marker failure: %v", statErr)
	}
}

func TestEnsureWritable(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(root, "new.proto")
	if err := ensureWritable(missing, false); err != nil {
		t.Errorf("missing path should be writable: %v", err)
	}

	existing := filepath.Join(root, "exists.proto")
	writeFixture(t, existing, "x")
	if err := ensureWritable(existing, false); err == nil {
		t.Error("existing file without --force should be rejected")
	}
	if err := ensureWritable(existing, true); err != nil {
		t.Errorf("existing file with --force should be writable: %v", err)
	}

	// A directory at the target path can never be written as a file — reject it
	// in preflight even with --force, rather than failing mid-generation.
	dir := filepath.Join(root, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritable(dir, true); err == nil {
		t.Error("directory target should be rejected even with --force")
	}
}

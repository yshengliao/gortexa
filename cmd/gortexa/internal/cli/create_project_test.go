package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupLayoutRepo builds a minimal local gortexa layout: a git repo on branch
// main whose go.mod declares the upstream layout module, cloneable via file://.
func setupLayoutRepo(t *testing.T) string {
	t.Helper()
	layout := t.TempDir()
	gitRun(t, layout, "-c", "init.defaultBranch=main", "init", "-q")
	writeFixture(t, filepath.Join(layout, "go.mod"), "module "+layoutModule+"\n\ngo 1.27.0\n")
	writeFixture(t, filepath.Join(layout, "main.go"),
		"package main\n\nimport _ \""+layoutModule+"/internal/logic\"\n\n// docs: https://"+layoutModule+"\n")
	// Repo meta that createProject must prune or replace.
	writeFixture(t, filepath.Join(layout, "cmd", "gortexa", "main.go"), "package main\n")
	writeFixture(t, filepath.Join(layout, "install.sh"), "#!/bin/sh\n")
	writeFixture(t, filepath.Join(layout, "README.md"), "# gortexa framework readme\n")
	// The config a scaffold inherits, carrying the placeholder secret the server
	// refuses to boot with. create must leave it exactly as it is.
	writeFixture(t, filepath.Join(layout, "etc", "config.yaml"),
		"auth:\n  jwt_secret: \""+devPlaceholderSecret+"\"\n")
	// The api submodule: a project consumes it from the proxy rather than
	// regenerating the annotations descriptor, so create must prune the module
	// itself while leaving the .proto (buf needs it to resolve the import) and
	// every reference to its import path pointing upstream.
	writeFixture(t, filepath.Join(layout, "api", "go.mod"), "module "+layoutModule+apiSubmodule+"\n\ngo 1.27.0\n")
	writeFixture(t, filepath.Join(layout, "api", "buf.gen.yaml"), "version: v2\n")
	writeFixture(t, filepath.Join(layout, "api", "gen", "gortexa", "ai", "v1", "annotations.pb.go"), "package aiv1\n")
	writeFixture(t, filepath.Join(layout, "proto", "gortexa", "ai", "v1", "annotations.proto"),
		"package gortexa.ai.v1;\n\noption go_package = \""+layoutModule+apiSubmodule+"/gen/gortexa/ai/v1;aiv1\";\n")
	writeFixture(t, filepath.Join(layout, "mcp", "ir.go"),
		"package mcp\n\nimport aiv1 \""+layoutModule+apiSubmodule+"/gen/gortexa/ai/v1\"\n\nvar _ = aiv1.E_AiTool\n")
	gitRun(t, layout, "add", "-A")
	gitRun(t, layout, "commit", "-q", "-m", "layout")
	return layout
}

// TestCreateProjectConsumesAPIModule pins the invariant that keeps a generated
// project able to depend on the framework at all: exactly one copy of
// gortexa/ai/v1/annotations.proto may reach a binary. The project therefore
// consumes the api module from the proxy — its go.mod keeps the require but
// loses the layout's local replace — while every import of that module, and the
// go_package the descriptor is generated under, must survive the module rewrite
// pointing upstream. The .proto stays so buf can still resolve the import.
func TestCreateProjectConsumesAPIModule(t *testing.T) {
	layout := setupLayoutRepo(t)
	writeFixture(t, filepath.Join(layout, "go.mod"),
		"module "+layoutModule+"\n\ngo 1.27.0\n\nrequire "+layoutModule+apiSubmodule+" v0.0.0\n\nreplace "+layoutModule+apiSubmodule+" => ./api\n")
	gitRun(t, layout, "commit", "-qam", "api require")
	dest := filepath.Join(t.TempDir(), "app")
	if err := createProject(dest, "example.com/app", "file://"+layout, "main"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dest, "api")); !os.IsNotExist(err) {
		t.Error("api/ must be pruned: a project consumes the published module instead of regenerating the descriptor")
	}
	upstreamAPI := layoutModule + apiSubmodule
	for _, tc := range []struct{ file, want string }{
		{filepath.Join("proto", "gortexa", "ai", "v1", "annotations.proto"), upstreamAPI + "/gen/gortexa/ai/v1;aiv1"},
		{filepath.Join("mcp", "ir.go"), upstreamAPI + "/gen/gortexa/ai/v1"},
	} {
		b, err := os.ReadFile(filepath.Join(dest, tc.file))
		if err != nil {
			t.Fatalf("%s must survive create: %v", tc.file, err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s must still reference %q (rewriting it would compile a second copy of the descriptor), got:\n%s", tc.file, tc.want, b)
		}
	}

	b, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "require "+upstreamAPI) {
		t.Errorf("go.mod must keep the api require, got:\n%s", b)
	}
	if strings.Contains(string(b), "replace "+upstreamAPI) {
		t.Errorf("go.mod must drop the layout's local replace (there is no ./api in a project), got:\n%s", b)
	}
}

func TestCreateProject(t *testing.T) {
	layout := setupLayoutRepo(t)
	dest := filepath.Join(t.TempDir(), "proj")

	if err := createProject(dest, "github.com/me/x", "file://"+layout, "main"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(dest, "go.mod")); !strings.Contains(got, "module github.com/me/x") {
		t.Errorf("go.mod not rewritten:\n%s", got)
	}
	main := readFile(t, filepath.Join(dest, "main.go"))
	if !strings.Contains(main, `"github.com/me/x/internal/logic"`) {
		t.Errorf("import path not rewritten:\n%s", main)
	}
	if !strings.Contains(main, "https://"+layoutModule) {
		t.Errorf("https URL to the upstream repo should be preserved:\n%s", main)
	}
	// The cloned history is dropped and a fresh repo is initialized.
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("expected a fresh .git in the new project: %v", err)
	}
	// Framework repo meta is pruned: the CLI source and installer must not ship
	// inside a generated project.
	for _, p := range []string{"cmd/gortexa", "install.sh"} {
		if _, err := os.Stat(filepath.Join(dest, p)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned from the new project", p)
		}
	}
	// The framework README is replaced with a project README naming the module.
	readme := readFile(t, filepath.Join(dest, "README.md"))
	if !strings.Contains(readme, "github.com/me/x") || strings.Contains(readme, "framework readme") {
		t.Errorf("project README not written:\n%s", readme)
	}
}

func TestCreateProjectDestExists(t *testing.T) {
	dest := t.TempDir()
	if err := createProject(dest, "github.com/me/x", "file:///nowhere", "main"); err == nil {
		t.Error("existing destination expected error")
	}
}

func TestCreateProjectCloneFails(t *testing.T) {
	layout := setupLayoutRepo(t)
	dest := filepath.Join(t.TempDir(), "proj")
	if err := createProject(dest, "github.com/me/x", "file://"+layout, "no-such-ref"); err == nil {
		t.Error("clone of a missing ref expected error")
	}
}

func TestCreateCmdDefaultModule(t *testing.T) {
	layout := setupLayoutRepo(t)
	dest := filepath.Join(t.TempDir(), "shipping")

	cmd := newCreateCmd()
	cmd.SetArgs([]string{dest, "--repo", "file://" + layout})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dest, "go.mod")); !strings.Contains(got, "module github.com/example/shipping") {
		t.Errorf("default module should derive from the destination base name:\n%s", got)
	}
}

func TestIsBinary(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'a'
	}
	nulAfter512 := make([]byte, 600)
	for i := range nulAfter512 {
		nulAfter512[i] = 'a'
	}
	nulAfter512[550] = 0

	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "empty", in: nil, want: false},
		{name: "plain text", in: []byte("hello"), want: false},
		{name: "NUL in first 512 bytes", in: []byte("he\x00llo"), want: true},
		{name: "long text without NUL", in: long, want: false},
		{name: "NUL only after 512 bytes", in: nulAfter512, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.in); got != tt.want {
				t.Errorf("isBinary = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRewriteModulePathSkipsNonTextFiles(t *testing.T) {
	root := t.TempDir()
	oldMod := layoutModule

	binContent := append([]byte("\x00binary "), []byte(oldMod)...)
	binPath := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(binPath, binContent, 0o644); err != nil {
		t.Fatal(err)
	}

	huge := strings.Repeat("x", (4<<20)+1) + oldMod
	hugePath := filepath.Join(root, "huge.txt")
	if err := os.WriteFile(hugePath, []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}

	// A symlink pointing outside root must be skipped, not followed.
	outside := filepath.Join(t.TempDir(), "target.go")
	if err := os.WriteFile(outside, []byte("import \""+oldMod+"/x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}

	// A .git dir is pruned entirely.
	gitFile := filepath.Join(root, ".git", "config")
	writeFixture(t, gitFile, "url = "+oldMod+"\n")

	if err := rewriteModulePath(root, oldMod, "example.com/demo"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, want string
	}{
		{"binary file untouched", binPath, string(binContent)},
		{"oversized file untouched", hugePath, huge},
		{"symlink target untouched", outside, "import \"" + oldMod + "/x\"\n"},
		{".git contents untouched", gitFile, "url = " + oldMod + "\n"},
	} {
		if got := readFile(t, tc.path); got != tc.want {
			t.Errorf("%s: file was rewritten", tc.name)
		}
	}
}

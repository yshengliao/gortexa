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
	writeFixture(t, filepath.Join(layout, "go.mod"), "module "+layoutModule+"\n\ngo 1.26.0\n")
	writeFixture(t, filepath.Join(layout, "main.go"),
		"package main\n\nimport _ \""+layoutModule+"/internal/logic\"\n\n// docs: https://"+layoutModule+"\n")
	gitRun(t, layout, "add", "-A")
	gitRun(t, layout, "commit", "-q", "-m", "layout")
	return layout
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

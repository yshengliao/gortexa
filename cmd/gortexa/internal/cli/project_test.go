package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRun runs git with a deterministic identity in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFindModuleRoot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.26.0\n")
	nested := filepath.Join(root, "internal", "logic")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		dir     string
		wantErr bool
		module  string
	}{
		{name: "at root", dir: root, module: "example.com/demo"},
		{name: "nested dir walks up", dir: nested, module: "example.com/demo"},
		{name: "outside any module", dir: t.TempDir(), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoot, gotMod, err := findModuleRoot(tt.dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("findModuleRoot(%q) expected error", tt.dir)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotRoot != root || gotMod != tt.module {
				t.Errorf("findModuleRoot(%q) = (%q, %q), want (%q, %q)", tt.dir, gotRoot, gotMod, root, tt.module)
			}
		})
	}
}

func TestReadModulePath(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.mod")
	writeFixture(t, valid, "// comment\nmodule   example.com/spaced  \n\ngo 1.26.0\n")
	noModule := filepath.Join(dir, "nomodule.mod")
	writeFixture(t, noModule, "go 1.26.0\n")

	tests := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{name: "valid go.mod trims spaces", path: valid, want: "example.com/spaced", ok: true},
		{name: "file without module line", path: noModule, ok: false},
		{name: "missing file", path: filepath.Join(dir, "absent.mod"), ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := readModulePath(tt.path)
			if ok != tt.ok || got != tt.want {
				t.Errorf("readModulePath(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRunCmd(t *testing.T) {
	dir := t.TempDir()
	if err := runCmd(dir, "true"); err != nil {
		t.Errorf("runCmd(true) = %v, want nil", err)
	}
	if err := runCmd(dir, "false"); err == nil {
		t.Error("runCmd(false) expected error")
	}
}

func TestMustGetwd(t *testing.T) {
	dir := t.TempDir()
	// macOS TempDir may be a symlink; resolve both sides for comparison.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got, err := filepath.EvalSymlinks(mustGetwd())
	if err != nil {
		t.Fatal(err)
	}
	if got != resolved {
		t.Errorf("mustGetwd() = %q, want %q", got, resolved)
	}
}

func TestGitRefExists(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "-c", "init.defaultBranch=main", "init", "-q")
	writeFixture(t, filepath.Join(root, "README.md"), "hello\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "init")

	if !gitRefExists(root, "main") {
		t.Error("gitRefExists(main) = false, want true")
	}
	if gitRefExists(root, "no-such-ref") {
		t.Error("gitRefExists(no-such-ref) = true, want false")
	}
	if gitRefExists(t.TempDir(), "main") {
		t.Error("gitRefExists outside a repo = true, want false")
	}
}

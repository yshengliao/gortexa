package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// setupRegenRoot creates a module root plus a PATH dir holding a fake buf whose
// behavior is driven by bufBody, and points PATH at it (keeping the real git
// reachable via gitToo when a repo is involved).
func setupRegenRoot(t *testing.T, bufBody string, gitToo bool) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
	bin := t.TempDir()
	writeScript(t, bin, "buf", bufBody)
	path := bin
	if gitToo {
		path += string(os.PathListSeparator) + os.Getenv("PATH")
	}
	t.Setenv("PATH", path)
	return root
}

func TestRegenBufMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir: no buf anywhere on PATH
	if err := regen(t.TempDir(), false); err == nil {
		t.Error("regen without buf on PATH expected error")
	}
}

func TestRegenPipeline(t *testing.T) {
	tests := []struct {
		name          string
		bufBody       string // fake buf: "$1" is the subcommand
		gitMain       bool   // create a git repo with a main branch
		allowBreaking bool
		wantErr       bool
	}{
		{name: "lint fails", bufBody: `[ "$1" = lint ] && exit 1; exit 0`, wantErr: true},
		{name: "no git repo skips breaking", bufBody: `[ "$1" = breaking ] && exit 1; exit 0`, wantErr: false},
		{name: "breaking fails against main", bufBody: `[ "$1" = breaking ] && exit 1; exit 0`, gitMain: true, wantErr: true},
		{name: "allow-breaking skips the gate", bufBody: `[ "$1" = breaking ] && exit 1; exit 0`, gitMain: true, allowBreaking: true, wantErr: false},
		{name: "generate fails", bufBody: `[ "$1" = generate ] && exit 1; exit 0`, wantErr: true},
		{name: "all green", bufBody: "exit 0", gitMain: true, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setupRegenRoot(t, tt.bufBody, tt.gitMain)
			if tt.gitMain {
				gitRun(t, root, "-c", "init.defaultBranch=main", "init", "-q")
				gitRun(t, root, "add", "-A")
				gitRun(t, root, "commit", "-q", "-m", "init")
			}
			err := regen(root, tt.allowBreaking)
			if (err != nil) != tt.wantErr {
				t.Errorf("regen() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegenCmdOutsideModule(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := newRegenCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Error("regen outside a module expected error")
	}
}

func TestRegenCmdAllowBreakingFlag(t *testing.T) {
	root := setupRegenRoot(t, `[ "$1" = breaking ] && exit 1; exit 0`, true)
	gitRun(t, root, "-c", "init.defaultBranch=main", "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "init")
	t.Chdir(root)
	cmd := newRegenCmd()
	cmd.SetArgs([]string{"--allow-breaking"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("regen --allow-breaking = %v, want nil", err)
	}
}

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctorTools mirrors the tool list doctor probes for on PATH.
var doctorTools = []string{
	"buf", "protoc-gen-go", "protoc-gen-go-grpc", "protoc-gen-grpc-gateway",
	"protoc-gen-openapiv2", "sqlc", "govulncheck", "benchstat",
}

// writeScript drops an executable shell script named name into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written. Only fmt.Print* output from this process is captured. The pipe
// is drained in a goroutine so fn can write more than the pipe buffer (~64KB)
// without deadlocking on a full pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	outCh := make(chan string, 1)
	go func() {
		b, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("read captured stdout: %v", err)
		}
		outCh <- string(b)
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-outCh
}

func TestDoctor(t *testing.T) {
	tests := []struct {
		name    string
		tools   []string // scripts to place on PATH (besides the outcome of goToo)
		goToo   bool     // include a fake `go` binary
		wantErr bool
	}{
		{name: "everything present", tools: doctorTools, goToo: true, wantErr: false},
		{name: "proto tools missing", tools: nil, goToo: true, wantErr: true},
		{name: "go missing too", tools: doctorTools, goToo: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			if tt.goToo {
				writeScript(t, bin, "go", `echo "go version go1.27 fake"`)
			}
			for _, tool := range tt.tools {
				writeScript(t, bin, tool, "exit 0")
			}
			t.Setenv("PATH", bin)
			var err error
			out := captureStdout(t, func() { err = newDoctorCmd().RunE(nil, nil) })
			if (err != nil) != tt.wantErr {
				t.Fatalf("doctor error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !strings.Contains(out, "environment OK") {
				t.Errorf("doctor output missing OK line:\n%s", out)
			}
			if tt.wantErr && !strings.Contains(out, "[MISSING]") {
				t.Errorf("doctor output missing MISSING line:\n%s", out)
			}
		})
	}
}

// TestCommandsOutsideModule covers the RunE error path of every command that
// requires a module root when the working directory has no go.mod upward.
func TestCommandsOutsideModule(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "run", args: []string{"run"}},
		{name: "tools install", args: []string{"tools", "install"}},
		{name: "tools sync no args", args: []string{"tools", "sync"}},
		{name: "tools sync with arg", args: []string{"tools", "sync", "buf@v1.0.0"}},
		{name: "skills install", args: []string{"skills", "install"}},
		{name: "skills list", args: []string{"skills", "list"}},
		{name: "regen", args: []string{"regen"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			root := newRootCmd()
			root.SetArgs(tt.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			if err := root.Execute(); err == nil {
				t.Errorf("%v outside a module expected error", tt.args)
			}
		})
	}
}

// TestModuleCommandsSucceedWithFakeGo covers the happy RunE paths of run/tools
// by putting a no-op `go` script on PATH inside a temp module root.
func TestModuleCommandsSucceedWithFakeGo(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "run forwards args", args: []string{"run", "--", "-flag"}},
		{name: "tools install", args: []string{"tools", "install"}},
		{name: "tools sync upgrade all", args: []string{"tools", "sync"}},
		{name: "tools sync pins each arg", args: []string{"tools", "sync", "a@v1", "b@v2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
			bin := t.TempDir()
			writeScript(t, bin, "go", "exit 0")
			t.Setenv("PATH", bin)
			t.Chdir(root)
			cmd := newRootCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Errorf("%v = %v, want nil", tt.args, err)
			}
		})
	}
}

func TestToolsSyncStopsOnFirstFailure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
	bin := t.TempDir()
	writeScript(t, bin, "go", "exit 1")
	t.Setenv("PATH", bin)
	t.Chdir(root)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"tools", "sync", "a@v1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("tools sync with failing go expected error")
	}
}

func TestSkillsList(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
	for _, s := range []string{"proto-regen", "generating-apis"} {
		if err := os.MkdirAll(filepath.Join(root, ".skills", s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A plain file must not be listed as a skill.
	writeFixture(t, filepath.Join(root, ".skills", "README.md"), "not a skill")
	t.Chdir(root)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"skills", "list"})
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proto-regen", "generating-apis"} {
		if !strings.Contains(out, want) {
			t.Errorf("skills list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Errorf("skills list should skip plain files:\n%s", out)
	}
}

func TestSkillsListMissingDir(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
	t.Chdir(root)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"skills", "list"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("skills list without .skills dir expected error")
	}
}

func TestVersionCmd(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"version"})
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gortexa "+cliVersion()) {
		t.Errorf("version output = %q, want it to contain %q", out, "gortexa "+cliVersion())
	}
}

func TestGoMinorAtLeast(t *testing.T) {
	cases := []struct {
		ver   string
		minor int
		want  bool
	}{
		{"go1.27.0", 27, true},
		{"go1.27", 27, true},
		{"go1.26.4", 27, false},
		{"go1.26.4", 26, true},
		{"go1.21.0", 21, true},
		{"go1.18", 21, false},
		{"go1.18.10", 27, false},
		// Unparseable formats must not block doctor.
		{"devel +abc123", 27, true},
		{"go2.0.0", 27, true},
		{"", 27, true},
	}
	for _, c := range cases {
		if got := goMinorAtLeast(c.ver, c.minor); got != c.want {
			t.Errorf("goMinorAtLeast(%q, %d) = %v, want %v", c.ver, c.minor, got, c.want)
		}
	}
}

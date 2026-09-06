package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRootUnknownCommand(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"no-such-command"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil {
		t.Error("unknown command expected error")
	}
}

func TestRootRegistersSubcommands(t *testing.T) {
	root := newRootCmd()
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"create", "gen", "regen", "run", "tools", "skills", "doctor", "version"} {
		if !got[want] {
			t.Errorf("root command missing subcommand %q", want)
		}
	}
}

// TestCLIVersion pins the precedence: an -ldflags value is reported as-is, and
// without one the CLI never reports the raw "(devel)" placeholder or an empty
// string — a test binary has no module version, so the fallback path is what
// runs here.
func TestCLIVersion(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "v9.9.9"
	if got := cliVersion(); got != "v9.9.9" {
		t.Errorf("cliVersion() with ldflags override = %q, want v9.9.9", got)
	}

	version = "dev"
	got := cliVersion()
	if got == "" || got == "(devel)" {
		t.Errorf("cliVersion() without override = %q, want a non-empty, non-placeholder value", got)
	}
	if !strings.HasPrefix(got, "dev") && !strings.HasPrefix(got, "v") {
		t.Errorf("cliVersion() = %q, want a module version or a dev marker", got)
	}
}

func TestExecute(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"gortexa", "version"}
	out := captureStdout(t, func() {
		if err := Execute(); err != nil {
			t.Errorf("Execute() = %v, want nil", err)
		}
	})
	if out == "" {
		t.Error("Execute(version) produced no output")
	}
}

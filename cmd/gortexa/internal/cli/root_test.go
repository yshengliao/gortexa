package cli

import (
	"io"
	"os"
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

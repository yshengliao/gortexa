package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRendered(t *testing.T) {
	data, err := parseTarget("billing/v1", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	t.Run("unknown template", func(t *testing.T) {
		if err := writeRendered(filepath.Join(root, "out.txt"), "no-such.tmpl", data); err == nil {
			t.Error("unknown template expected error")
		}
	})

	t.Run("mkdir fails when a parent is a file", func(t *testing.T) {
		blocker := filepath.Join(root, "blocker")
		writeFixture(t, blocker, "not a dir")
		if err := writeRendered(filepath.Join(blocker, "sub", "x.proto"), "proto.tmpl", data); err == nil {
			t.Error("MkdirAll under a regular file expected error")
		}
	})
}

func TestRel(t *testing.T) {
	tests := []struct {
		name, root, path, want string
	}{
		{name: "relativizes under root", root: "/a", path: "/a/b/c", want: "b/c"},
		{name: "falls back to path on error", root: ".", path: "/abs/x", want: "/abs/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rel(tt.root, tt.path); got != tt.want {
				t.Errorf("rel(%q, %q) = %q, want %q", tt.root, tt.path, got, tt.want)
			}
		})
	}
}

// TestGenerateAPIRunsRegen covers the skipGen=false branch end-to-end with fake
// buf/gofmt binaries on PATH, and the regen-failure branch with an empty PATH.
func TestGenerateAPIRunsRegen(t *testing.T) {
	data, err := parseTarget("billing/v1", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	data.Module = "example.com/demo"

	t.Run("regen succeeds", func(t *testing.T) {
		root := setupFixtureProject(t)
		bin := t.TempDir()
		writeScript(t, bin, "buf", "exit 0")
		writeScript(t, bin, "gofmt", "exit 0")
		t.Setenv("PATH", bin)
		if err := generateAPI(root, data, genOpts{}); err != nil {
			t.Fatalf("generateAPI with fake buf = %v, want nil", err)
		}
	})

	t.Run("regen fails without buf", func(t *testing.T) {
		root := setupFixtureProject(t)
		t.Setenv("PATH", t.TempDir())
		if err := generateAPI(root, data, genOpts{}); err == nil {
			t.Error("generateAPI without buf on PATH expected regen error")
		}
	})
}

func TestGenCmd(t *testing.T) {
	t.Run("outside module", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd := newGenCmd()
		cmd.SetArgs([]string{"billing/v1", "Invoice"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Error("gen outside a module expected error")
		}
	})

	t.Run("invalid target", func(t *testing.T) {
		cmd := newGenCmd()
		cmd.SetArgs([]string{"Billing"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Error("invalid target expected error")
		}
	})

	t.Run("skip-gen no-wire inside fixture module", func(t *testing.T) {
		root := setupFixtureProject(t)
		t.Chdir(root)
		cmd := newGenCmd()
		cmd.SetArgs([]string{"billing/v1", "Invoice", "--skip-gen", "--no-wire"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "proto", "billing", "v1", "invoice.proto")); err != nil {
			t.Errorf("proto not written: %v", err)
		}
	})
}

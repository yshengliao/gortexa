package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProtoNamespaceSkipsMajorVersionAndGRPC pins two derivations that used to
// collide: every .../vN module got the namespace "vN", and "grpc" put a user
// service under the grpc.health/grpc.reflection packages the server exempts
// from authentication.
func TestProtoNamespaceSkipsMajorVersionAndGRPC(t *testing.T) {
	for _, tc := range []struct{ module, want string }{
		{"github.com/acme/billing/v2", "billing"},
		{"github.com/acme/shop/v10", "shop"},
		{"v2", "v2"}, // a lone element is the project name, not a version suffix
		{"github.com/acme/grpc", "grpcapp"},
		{"github.com/acme/grpc/v3", "grpcapp"},
	} {
		if got := protoNamespace(tc.module); got != tc.want {
			t.Errorf("protoNamespace(%q) = %q, want %q", tc.module, got, tc.want)
		}
	}
}

func TestParseTargetRejectsUnbuildableNames(t *testing.T) {
	for _, tgt := range []string{"internal/v1", "vendor/v1", "testdata/v1"} {
		if _, err := parseTarget(tgt, "Foo"); err == nil {
			t.Errorf("parseTarget(%q) expected error", tgt)
		}
	}
	for _, entity := range []string{"RobotArm", "NodeJs", "RobotAndroid", "FooWindows", "BarLinuxAmd64", "ChipWasm"} {
		if _, err := parseTarget("robotics/v1", entity); err == nil {
			t.Errorf("parseTarget entity %q expected a GOOS/GOARCH suffix error", entity)
		}
	}
	// A constraint word only counts after an underscore, and only as the last element.
	for _, entity := range []string{"Windows", "Linux", "ArmRest", "JsonDoc"} {
		if _, err := parseTarget("robotics/v1", entity); err != nil {
			t.Errorf("parseTarget entity %q = %v, want nil", entity, err)
		}
	}
}

func TestGenerateAPIRejectsReservedFlatDomain(t *testing.T) {
	for _, domain := range []string{"gortexa", "google", "buf"} {
		root := setupFixtureProject(t)
		data, err := parseTarget(domain+"/v1", "Foo")
		if err != nil {
			t.Fatal(err)
		}
		data.Module = "example.com/demo"
		if err := generateAPI(root, data, genOpts{skipGen: true, noWire: true}); err == nil {
			t.Errorf("flat domain %q expected error", domain)
		}
		// Under a namespace the same domain is an ordinary nested directory.
		data.Namespace = "shop"
		if err := generateAPI(root, data, genOpts{skipGen: true, noWire: true}); err != nil {
			t.Errorf("namespaced domain %q = %v, want nil", domain, err)
		}
	}
}

func TestGenCmdRejectsInvalidManifestNamespace(t *testing.T) {
	root := setupFixtureProject(t)
	if err := writeManifest(root, projectManifest{ProtoNamespace: "../../escape"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cmd := newGenCmd()
	cmd.SetArgs([]string{"billing/v1", "Invoice", "--skip-gen", "--no-wire"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("gen with a path-traversing proto_namespace expected error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Error("gen wrote outside the project")
	}
}

func TestRunStripsFlagTerminator(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.27.0\n")
	bin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	writeScript(t, bin, "go", `printf '%s\n' "$@" > "`+argsFile+`"`)
	t.Setenv("PATH", bin)
	t.Chdir(root)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"run", "--", "-export-ai-schemas=mcp"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(readFile(t, argsFile))
	want := []string{"run", "./cmd/server", "-export-ai-schemas=mcp"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("go args = %q, want %q", got, want)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://x-access-token:ghp_XXXX@github.com/acme/fork": "https://github.com/acme/fork",
		"https://github.com/acme/fork":                         "https://github.com/acme/fork",
		"git@github.com:acme/fork.git":                         "git@github.com:acme/fork.git",
		"file:///tmp/layout":                                   "file:///tmp/layout",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

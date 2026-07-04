package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseTargetRejectsDigitThenLower covers the digit-then-lowercase guard
// (S3bucket) and the snake-ends-in-_test guard (FooTest → foo_test), both added
// in the fix round. Each must be rejected with an error.
func TestParseTargetRejectsDigitThenLower(t *testing.T) {
	tests := []struct {
		name   string
		entity string
	}{
		{name: "digit then lowercase", entity: "S3bucket"},
		{name: "snake ends in _test", entity: "FooTest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseTarget("billing/v1", tt.entity); err == nil {
				t.Errorf("parseTarget(billing/v1, %q) expected error", tt.entity)
			}
		})
	}
	// The digit-then-UPPER form is fine and must be accepted.
	if _, err := parseTarget("billing/v1", "S3Bucket"); err != nil {
		t.Errorf("parseTarget(billing/v1, S3Bucket) = %v, want nil", err)
	}
}

func TestDigitThenLowerIndex(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{in: "S3bucket", want: 1},
		{in: "S3Bucket", want: -1},
		{in: "Invoice", want: -1},
		{in: "", want: -1},
		{in: "9a", want: 0},
		{in: "A9", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := digitThenLowerIndex(tt.in); got != tt.want {
				t.Errorf("digitThenLowerIndex(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidModulePath(t *testing.T) {
	tests := []struct {
		name   string
		module string
		want   bool
	}{
		{name: "valid github path", module: "github.com/me/app", want: true},
		{name: "single segment", module: "app", want: true},
		{name: "dots dashes underscores", module: "github.com/me-org/my_app.v2", want: true},
		{name: "empty", module: "", want: false},
		{name: "contains space", module: "github.com/me/ap p", want: false},
		{name: "contains colon", module: "github.com:me/app", want: false},
		{name: "contains at", module: "github.com/me/app@v1", want: false},
		{name: "empty path segment", module: "a//b", want: false},
		{name: "trailing slash", module: "github.com/me/app/", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validModulePath(tt.module); got != tt.want {
				t.Errorf("validModulePath(%q) = %v, want %v", tt.module, got, tt.want)
			}
		})
	}
}

func TestUniqueMask(t *testing.T) {
	got := uniqueMask("some file body without any mask token")
	if strings.Contains("some file body without any mask token", got) {
		t.Errorf("uniqueMask returned a token present in the input: %q", got)
	}
	if !strings.HasPrefix(got, "GORTEXA_MASK_") {
		t.Errorf("uniqueMask = %q, want a GORTEXA_MASK_* token", got)
	}
}

// TestRewriteModulePathPreservesLiteralMaskConstant guards the fix-round change
// that derives a per-file unique mask: a file that literally contains the old
// mask constant text ("GORTEXA_MASK_") alongside the module path must be
// rewritten for the module path only, leaving the mask literal intact.
func TestRewriteModulePathPreservesLiteralMaskConstant(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	content := "const mask = \"GORTEXA_MASK_\"\nmodule " + layoutModule + "\n"
	writeFixture(t, path, content)

	if err := rewriteModulePath(root, layoutModule, "example.com/demo"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "GORTEXA_MASK_") {
		t.Errorf("literal mask constant was corrupted:\n%s", got)
	}
	if !strings.Contains(got, "module example.com/demo") {
		t.Errorf("module path not rewritten:\n%s", got)
	}
	if strings.Contains(got, "module "+layoutModule) {
		t.Errorf("old module path still present:\n%s", got)
	}
}

func TestCreateProjectInvalidModule(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "proj")
	if err := createProject(dest, "not a module", "file:///nowhere", "main"); err == nil {
		t.Error("invalid --module expected early error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("invalid module must not create the destination")
	}
}

func TestFreshenJWTSecret(t *testing.T) {
	t.Run("placeholder replaced", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		writeFixture(t, cfg, "jwt:\n  secret: "+devPlaceholderSecret+"\n")

		if err := freshenJWTSecret(cfg); err != nil {
			t.Fatal(err)
		}
		got := readFile(t, cfg)
		if strings.Contains(got, devPlaceholderSecret) {
			t.Errorf("placeholder secret still present:\n%s", got)
		}
		// Extract the value after "secret: " and check it's a 48-hex string.
		idx := strings.Index(got, "secret: ")
		if idx < 0 {
			t.Fatalf("no secret line:\n%s", got)
		}
		val := strings.TrimSpace(got[idx+len("secret: "):])
		if len(val) != 48 {
			t.Errorf("secret length = %d, want 48 hex chars: %q", len(val), val)
		}
		for _, r := range val {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Errorf("secret is not lowercase hex: %q", val)
				break
			}
		}
	})

	t.Run("no placeholder left unchanged", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		orig := "jwt:\n  secret: already-custom-secret\n"
		writeFixture(t, cfg, orig)

		if err := freshenJWTSecret(cfg); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, cfg); got != orig {
			t.Errorf("config without placeholder should be untouched:\n%s", got)
		}
	})

	t.Run("missing file not an error", func(t *testing.T) {
		if err := freshenJWTSecret(filepath.Join(t.TempDir(), "absent.yaml")); err != nil {
			t.Errorf("missing config = %v, want nil", err)
		}
	})
}

// TestFindModuleRootPrefersRootWithBufGen builds a project root carrying both
// go.mod and buf.gen.yaml, plus a nested sub-module with its own bare go.mod and
// no buf.gen.yaml. Running from the sub-dir must resolve to the ROOT.
func TestFindModuleRootPrefersRootWithBufGen(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.26.0\n")
	writeFixture(t, filepath.Join(root, "buf.gen.yaml"), "version: v2\n")

	sub := filepath.Join(root, "tools")
	writeFixture(t, filepath.Join(sub, "go.mod"), "module example.com/demo/tools\n\ngo 1.26.0\n")

	gotRoot, gotMod, err := findModuleRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || gotMod != "example.com/demo" {
		t.Errorf("findModuleRoot(%q) = (%q, %q), want (%q, %q)",
			sub, gotRoot, gotMod, root, "example.com/demo")
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.txt")
	writeFixture(t, present, "x")

	if !fileExists(present) {
		t.Errorf("fileExists(%q) = false, want true", present)
	}
	if fileExists(filepath.Join(dir, "absent.txt")) {
		t.Error("fileExists on a missing path = true, want false")
	}
}

func TestIsGitToplevel(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "-c", "init.defaultBranch=main", "init", "-q")
	sub := filepath.Join(repo, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if !isGitToplevel(repo) {
		t.Error("isGitToplevel at repo root = false, want true")
	}
	if isGitToplevel(sub) {
		t.Error("isGitToplevel in a subdir of a repo = true, want false")
	}
	if isGitToplevel(t.TempDir()) {
		t.Error("isGitToplevel outside any repo = true, want false")
	}

	// A repo reached through a symlink must still count as toplevel: git
	// reports the physical path, so an unresolved comparison would silently
	// skip the breaking gate (as it did on macOS, where TMPDIR itself sits
	// behind the /var → /private/var symlink).
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if !isGitToplevel(link) {
		t.Error("isGitToplevel via symlinked path = false, want true")
	}
}

// TestRunCmdSetsCorrectedGoEnv verifies runCmd overrides the container's broken
// Go module env for the child process, even when the parent env sets a bad value.
func TestRunCmdSetsCorrectedGoEnv(t *testing.T) {
	t.Setenv("GOFLAGS", "BROKEN-INHERITED")
	t.Setenv("GOPROXY", "http://broken.invalid")

	dir := t.TempDir()
	bin := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	script := filepath.Join(bin, "dumpenv")
	writeScript(t, bin, "dumpenv", `printf 'GOFLAGS=%s\nGOPROXY=%s\n' "$GOFLAGS" "$GOPROXY" > "$1"`)

	if err := runCmd(dir, script, out); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, out)
	if !strings.Contains(got, "GOFLAGS=-mod=mod") {
		t.Errorf("child GOFLAGS not overridden:\n%s", got)
	}
	if !strings.Contains(got, "GOPROXY=https://proxy.golang.org,direct") {
		t.Errorf("child GOPROXY not overridden:\n%s", got)
	}
	if strings.Contains(got, "BROKEN-INHERITED") || strings.Contains(got, "broken.invalid") {
		t.Errorf("broken inherited env leaked to child:\n%s", got)
	}
}

// setupGitFixtureProject builds a wireable fixture project that is also a git
// repo on branch main, with a fake buf (driven by bufBody) plus the real git and
// gofmt reachable on PATH.
func setupGitFixtureProject(t *testing.T, bufBody string) string {
	t.Helper()
	root := setupFixtureProject(t)
	gitRun(t, root, "-c", "init.defaultBranch=main", "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "init")

	bin := t.TempDir()
	writeScript(t, bin, "buf", bufBody)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

// TestGenerateAPIForceImpliesAllowBreaking verifies the fix-round plumbing:
// generateAPI(force=true) calls regen with allow-breaking, so a fake buf whose
// breaking gate fails does not abort generation; force=false surfaces the gate.
func TestGenerateAPIForceImpliesAllowBreaking(t *testing.T) {
	data, err := parseTarget("billing/v1", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	data.Module = "example.com/demo"
	breakingFails := `[ "$1" = breaking ] && exit 1; exit 0`

	t.Run("force skips breaking gate", func(t *testing.T) {
		root := setupGitFixtureProject(t, breakingFails)
		if err := generateAPI(root, data, genOpts{force: true}); err != nil {
			t.Errorf("generateAPI(force) = %v, want nil (force implies allow-breaking)", err)
		}
	})

	t.Run("without force the breaking gate fails", func(t *testing.T) {
		root := setupGitFixtureProject(t, breakingFails)
		if err := generateAPI(root, data, genOpts{}); err == nil {
			t.Error("generateAPI without force expected the breaking gate to fail")
		}
	})

	t.Run("explicit allow-breaking skips the gate", func(t *testing.T) {
		root := setupGitFixtureProject(t, breakingFails)
		if err := generateAPI(root, data, genOpts{allowBreaking: true}); err != nil {
			t.Errorf("generateAPI(allowBreaking) = %v, want nil", err)
		}
	})
}

// TestGenCmdAllowBreakingFlag drives newGenCmd via SetArgs with a fake buf whose
// breaking gate fails, asserting --allow-breaking and --force both let it pass
// while the bare invocation fails.
func TestGenCmdAllowBreakingFlag(t *testing.T) {
	breakingFails := `[ "$1" = breaking ] && exit 1; exit 0`
	tests := []struct {
		name    string
		flag    string
		wantErr bool
	}{
		{name: "allow-breaking passes", flag: "--allow-breaking", wantErr: false},
		{name: "force passes", flag: "--force", wantErr: false},
		{name: "bare invocation fails on gate", flag: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setupGitFixtureProject(t, breakingFails)
			t.Chdir(root)
			cmd := newGenCmd()
			args := []string{"billing/v1", "Invoice"}
			if tt.flag != "" {
				args = append(args, tt.flag)
			}
			cmd.SetArgs(args)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			err := cmd.Execute()
			if (err != nil) != tt.wantErr {
				t.Errorf("gen %v = %v, wantErr %v", args, err, tt.wantErr)
			}
		})
	}
}

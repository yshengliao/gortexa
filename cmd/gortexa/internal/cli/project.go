package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// findModuleRoot walks up from dir to the gortexa project root, returning it and
// its module path. It prefers a directory that has both go.mod and buf.gen.yaml
// (the project root) over a nearer bare go.mod, so running inside the nested
// tools/ module resolves to the project — not to tools/ itself. If no ancestor
// has buf.gen.yaml, it falls back to the nearest go.mod.
func findModuleRoot(dir string) (root, module string, err error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	var fbRoot, fbMod string
	for {
		if mod, ok := readModulePath(filepath.Join(d, "go.mod")); ok {
			if fileExists(filepath.Join(d, "buf.gen.yaml")) {
				return d, mod, nil
			}
			if fbRoot == "" {
				fbRoot, fbMod = d, mod
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			if fbRoot != "" {
				return fbRoot, fbMod, nil
			}
			return "", "", fmt.Errorf("no go.mod found from %s upward — run inside a gortexa project", dir)
		}
		d = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readModulePath(gomod string) (string, bool) {
	f, err := os.Open(gomod)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// goModuleEnv is the corrected Go module env (mirroring the Makefile) so
// `go`-invoking subcommands like `tools install` don't 403 against the
// container's broken GOPROXY/GOPRIVATE defaults. Non-Go tools (git, buf)
// simply ignore these. Later entries win in os/exec, so appending these to
// os.Environ() overrides any inherited values.
var goModuleEnv = []string{
	"GOFLAGS=-mod=mod",
	"GOPROXY=https://proxy.golang.org,direct",
	"GOSUMDB=sum.golang.org",
	"GOTOOLCHAIN=auto",
	"GOPRIVATE=",
	"GOINSECURE=",
}

// runCmd runs name+args in dir, streaming stdio to the parent process.
func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(), goModuleEnv...)
	return cmd.Run()
}

// runCmdOutput is runCmd capturing stdout (stderr still streams) for commands
// whose output is the artifact.
func runCmdOutput(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), goModuleEnv...)
	return cmd.Output()
}

func mustGetwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

// gitRefExists reports whether ref resolves in the repo at root.
func gitRefExists(root, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = root
	return cmd.Run() == nil
}

// isGitToplevel reports whether root is itself the top of a git work tree.
// `buf breaking --against .git#branch=main` reads .git at the project root, so
// the breaking gate must be skipped (not hard-fail) when root sits inside a
// larger repo rather than being the repo root.
func isGitToplevel(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// git prints the physical (symlink-resolved) toplevel, while root may be a
	// logical spelling of the same directory (macOS /var → /private/var, or a
	// project reached through a symlink). Resolve both sides before comparing,
	// or the breaking gate silently skips on such paths.
	top, err := resolveDir(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	abs, err := resolveDir(root)
	return err == nil && top == abs
}

// resolveDir returns the symlink-free absolute form of p.
func resolveDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// findModuleRoot walks up from dir to the directory whose go.mod declares a
// top-level module path (the project root), returning the root and module path.
func findModuleRoot(dir string) (root, module string, err error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		if mod, ok := readModulePath(filepath.Join(d, "go.mod")); ok {
			return d, mod, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", fmt.Errorf("no go.mod found from %s upward — run inside a gortexa project", dir)
		}
		d = parent
	}
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

// runCmd runs name+args in dir, streaming stdio to the parent process.
func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
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

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// layoutModule is the module path of the gortexa layout that `create` clones and
// rewrites into the new project's module path.
const layoutModule = "github.com/yshengliao/gortexa"

func newCreateCmd() *cobra.Command {
	var module, ref, repo string
	cmd := &cobra.Command{
		Use:   "create <path>",
		Short: "Scaffold a new Gortexa project (clone the layout, rewrite the module path)",
		Long: "create clones the Gortexa layout into <path>, drops its git history, and\n" +
			"rewrites the module path so you get a working, batteries-included project\n" +
			"(one h2c port: gRPC + HTTP/JSON + MCP) with a sample resource to learn from.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dest := args[0]
			if module == "" {
				module = "github.com/example/" + filepath.Base(filepath.Clean(dest))
			}
			return createProject(dest, module, repo, ref)
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "go module path for the new project (default github.com/example/<name>)")
	cmd.Flags().StringVar(&ref, "ref", "main", "git ref of the gortexa layout to clone")
	cmd.Flags().StringVar(&repo, "repo", "https://github.com/yshengliao/gortexa", "gortexa layout repository URL")
	return cmd
}

func createProject(dest, module, repo, ref string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination %q already exists", dest)
	}
	fmt.Printf("==> cloning %s (%s) → %s\n", repo, ref, dest)
	if err := runCmd(".", "git", "clone", "--depth", "1", "--branch", ref, repo, dest); err != nil {
		return fmt.Errorf("clone layout: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(dest, ".git")); err != nil {
		return fmt.Errorf("remove cloned .git: %w", err)
	}
	fmt.Printf("==> rewriting module path %s → %s\n", layoutModule, module)
	if err := rewriteModulePath(dest, layoutModule, module); err != nil {
		return fmt.Errorf("rewrite module path: %w", err)
	}
	if err := runCmd(dest, "git", "init", "-q"); err != nil {
		fmt.Fprintln(os.Stderr, "warning: git init failed:", err)
	}
	fmt.Printf("\n==> created %s\n", dest)
	fmt.Printf("    next: cd %s && make bootstrap && make gen && make run\n", dest)
	return nil
}

// rewriteModulePath replaces every occurrence of oldMod with newMod across the
// project's text files (go.mod, tools/go.mod, *.go, *.proto, docs). gen/ is
// gitignored and absent from a fresh clone, so generated code is later produced
// against newMod by `make gen`.
func rewriteModulePath(root, oldMod, newMod string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		// Skip symlinks (e.g. the .claude/.codex/.github/.agents skill links),
		// devices and oversized/unreadable files — only rewrite regular text files.
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil //nolint:nilerr // skipping a non-regular/unreadable file is not fatal
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(b) || !strings.Contains(string(b), oldMod) {
			return nil
		}
		s := string(b)
		// Avoid blindly rewriting HTTPS URLs (like the github repo in README.md)
		// by temporarily masking them.
		const httpsMask = "HTTPS_GORTEXA_REPO_MASK_RESERVED"
		s = strings.ReplaceAll(s, "https://"+oldMod, httpsMask)
		s = strings.ReplaceAll(s, oldMod, newMod)
		s = strings.ReplaceAll(s, httpsMask, "https://"+oldMod)

		return os.WriteFile(path, []byte(s), info.Mode().Perm())
	})
}

// isBinary reports whether b looks binary (contains a NUL in its first 512 bytes).
func isBinary(b []byte) bool {
	if len(b) > 512 {
		b = b[:512]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

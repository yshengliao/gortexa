package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if !validModulePath(module) {
		return fmt.Errorf("invalid --module %q: expected a Go module path like github.com/me/app", module)
	}
	// Reject a --repo/--ref that git would parse as an option (e.g.
	// "--upload-pack=...") — argument injection into the clone. The "--"
	// terminator below is the primary guard; this rejects the shape outright
	// with a clear message rather than letting git fail obscurely.
	if strings.HasPrefix(repo, "-") {
		return fmt.Errorf("invalid --repo %q: must not begin with '-'", repo)
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid --ref %q: must not begin with '-'", ref)
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination %q already exists", dest)
	}
	fmt.Printf("==> cloning %s (%s) → %s\n", repo, ref, dest)
	// "--" ends option parsing so repo/dest can never be read as git flags.
	if err := runCmd(".", "git", "clone", "--depth", "1", "--branch", ref, "--", repo, dest); err != nil {
		return fmt.Errorf("clone layout: %w", err)
	}
	// From here the directory exists and is ours; remove it on any failure so a
	// half-created project doesn't block a retry (the dest-exists guard above).
	cleanup := func(err error) error {
		_ = os.RemoveAll(dest)
		return err
	}
	if err := os.RemoveAll(filepath.Join(dest, ".git")); err != nil {
		return cleanup(fmt.Errorf("remove cloned .git: %w", err))
	}
	// Prune repo meta that must not ship inside a generated project: the CLI's
	// own source tree and the bootstrap installer belong to the framework repo,
	// and the module rewrite below would corrupt the install instructions they
	// contain. gen/ is pruned because the framework commits it — a naive string
	// rewrite inside a .pb.go length-prefixed rawDesc would corrupt the proto
	// descriptor; the project regenerates all of gen/ via `make gen`. The
	// framework README is replaced with a project README after the rewrite.
	for _, p := range []string{"cmd/gortexa", "install.sh", "gen"} {
		if err := os.RemoveAll(filepath.Join(dest, p)); err != nil {
			return cleanup(fmt.Errorf("prune %s: %w", p, err))
		}
	}
	fmt.Printf("==> rewriting module path %s → %s\n", layoutModule, module)
	if err := rewriteModulePath(dest, layoutModule, module); err != nil {
		return cleanup(fmt.Errorf("rewrite module path: %w", err))
	}
	// Replace the placeholder JWT secret so the scaffolded project boots and is
	// not born using the publicly-known dev key.
	if err := freshenJWTSecret(filepath.Join(dest, "etc", "config.yaml")); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not set a fresh jwt secret:", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte(projectReadme(module)), 0o644); err != nil {
		return cleanup(fmt.Errorf("write project README: %w", err))
	}
	// -b main: the copied CI workflow triggers on (and fetches) main; without
	// pinning, a machine whose git still defaults to master gets a scaffold
	// whose CI never runs on push and fails the origin/main fetch on PRs.
	if err := runCmd(dest, "git", "init", "-q", "-b", "main"); err != nil {
		fmt.Fprintln(os.Stderr, "warning: git init failed:", err)
	}
	fmt.Printf("\n==> created %s\n", dest)
	fmt.Printf("    next: cd %s && make bootstrap && make gen && make run\n", dest)
	return nil
}

// rewriteModulePath replaces every occurrence of oldMod with newMod across the
// project's text files (go.mod, tools/go.mod, *.go, *.proto, docs). gen/ is
// pruned from the clone before this runs, so generated code is later produced
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
		// by temporarily masking them. The mask token must be absent from the file
		// — a fixed literal would itself be rewritten in any file that contains it
		// (e.g. this very source), so derive a unique one per file.
		mask := uniqueMask(s)
		s = strings.ReplaceAll(s, "https://"+oldMod, mask)
		s = strings.ReplaceAll(s, oldMod, newMod)
		s = strings.ReplaceAll(s, mask, "https://"+oldMod)

		return os.WriteFile(path, []byte(s), info.Mode().Perm())
	})
}

// devPlaceholderSecret mirrors config: the value the server refuses to
// boot with. `create` swaps it for a fresh random secret in the new project.
const devPlaceholderSecret = "dev-only-insecure-secret-change-me-please"

// freshenJWTSecret replaces the committed placeholder secret in a scaffolded
// project's config with a fresh random 48-byte hex value. A missing config file
// or an absent placeholder is not an error (nothing to do).
func freshenJWTSecret(configPath string) error {
	b, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !strings.Contains(string(b), devPlaceholderSecret) {
		return nil
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	secret := hex.EncodeToString(raw[:]) // 48 hex chars, well over the 32-byte floor
	out := strings.ReplaceAll(string(b), devPlaceholderSecret, secret)
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, []byte(out), info.Mode().Perm())
}

// validModulePath does a lightweight check that module looks like a Go module
// path (a slash-separated set of non-empty segments of safe characters, no
// spaces or scheme). It intentionally rejects obviously bad input before that
// value is rewritten across every file in the project.
func validModulePath(module string) bool {
	if module == "" || strings.ContainsAny(module, " \t\n:@") {
		return false
	}
	for seg := range strings.SplitSeq(module, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '.' || r == '-' || r == '_' || r == '~':
			default:
				return false
			}
		}
	}
	return true
}

// uniqueMask returns a placeholder token guaranteed not to appear in s.
func uniqueMask(s string) string {
	for {
		var raw [12]byte
		_, _ = rand.Read(raw[:])
		mask := "GORTEXA_MASK_" + hex.EncodeToString(raw[:])
		if !strings.Contains(s, mask) {
			return mask
		}
	}
}

// isBinary reports whether b looks binary (contains a NUL in its first 512 bytes).
func isBinary(b []byte) bool {
	if len(b) > 512 {
		b = b[:512]
	}
	return slices.Contains(b, 0)
}

// projectReadme is the README written into a freshly created project, replacing
// the framework's own README (whose install instructions would otherwise be
// corrupted by the module rewrite).
func projectReadme(module string) string {
	return fmt.Sprintf(`# %s

A [Gortexa](https://github.com/yshengliao/gortexa) service: one h2c port serves
gRPC, HTTP/JSON (grpc-gateway) and MCP, sharing one interceptor chain, one
error model and one auth path.

## Develop

`+"```bash"+`
make bootstrap   # install the pinned toolchain
make gen         # buf lint -> breaking -> generate
make run         # dev server on :8080
make test
`+"```"+`

## Layout

- `+"`proto/`"+` — API contracts (the single source of truth; regenerate after edits)
- `+"`internal/logic/`"+` — your business logic
- `+"`cmd/server/`"+` — server wiring
- `+"`etc/config.yaml`"+` — configuration
- `+"`gen/`"+` — generated code: never hand-edit; regenerate with `+"`make gen`"+` (committed — the copied CI guards drift)

Docs: https://gortexa.sheng.page
`, module)
}

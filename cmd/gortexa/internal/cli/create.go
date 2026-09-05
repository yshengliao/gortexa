package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// layoutModule is the module path of the gortexa layout that `create` clones and
// rewrites into the new project's module path.
const layoutModule = "github.com/yshengliao/gortexa"

// apiSubmodule is the suffix of the standalone module that owns the
// gortexa.ai.v1 annotations proto and its Go bindings. A generated project
// depends on that module instead of carrying its own copy, so the module
// rewrite must leave its import path pointing upstream: protobuf's global
// registry is keyed on the proto file path, so a second copy of
// gortexa/ai/v1/annotations.proto panics at init.
const apiSubmodule = "/api"

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
	// api/ holds the generated gortexa.ai.v1 bindings and its own go.mod. A
	// project consumes that module from the proxy instead of regenerating the
	// descriptor — two copies of gortexa/ai/v1/annotations.proto in one binary
	// panic at init. proto/gortexa/ai/v1/annotations.proto is deliberately NOT
	// pruned: buf still has to resolve `import "gortexa/ai/v1/annotations.proto"`
	// for the project's own protos. Dropping api/buf.gen.yaml with it is what
	// makes regen skip the api generate step in a scaffolded project.
	for _, p := range []string{"cmd/gortexa", "install.sh", "gen", "api"} {
		if err := os.RemoveAll(filepath.Join(dest, p)); err != nil {
			return cleanup(fmt.Errorf("prune %s: %w", p, err))
		}
	}
	fmt.Printf("==> rewriting module path %s → %s\n", layoutModule, module)
	if err := rewriteModulePath(dest, layoutModule, module); err != nil {
		return cleanup(fmt.Errorf("rewrite module path: %w", err))
	}
	ns := protoNamespace(module)
	fmt.Printf("==> namespacing sample service under %s.resource.v1\n", ns)
	if err := namespaceSample(dest, ns); err != nil {
		return cleanup(fmt.Errorf("namespace sample service: %w", err))
	}
	if err := writeManifest(dest, projectManifest{
		CLIVersion:     version,
		ModulePath:     module,
		ProtoNamespace: ns,
		SourceRepo:     repo,
		SourceRef:      ref,
	}); err != nil {
		return cleanup(fmt.Errorf("write project manifest: %w", err))
	}

	// The layout replaces the api module with ./api so the framework repo builds
	// before that module is tagged. A generated project has no ./api, so it must
	// resolve the require from the proxy instead.
	if err := runCmd(dest, "go", "mod", "edit", "-dropreplace="+layoutModule+apiSubmodule); err != nil {
		return cleanup(fmt.Errorf("drop api replace directive: %w", err))
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
	apiMod := oldMod + apiSubmodule
	// The api module path, but only where it ends a path element: followed by a
	// separator, by a delimiter such as a quote or space, or by end of input.
	// Matching it as a bare substring would be the same missing-boundary defect
	// the mask exists to fix, and would leave a sibling package like
	// ".../apiutil" pointing upstream after the rewrite. A "." is treated as a
	// delimiter: a path element continuing past a dot is rare, while the path
	// ending a sentence in prose is not. The trailing character is captured so
	// it survives the substitution.
	apiRe := regexp.MustCompile(regexp.QuoteMeta(apiMod) + `([^A-Za-z0-9_-]|$)`)
	return rewriteTextFiles(root, func(s string) string {
		if !strings.Contains(s, oldMod) {
			return s
		}
		// Avoid blindly rewriting HTTPS URLs (like the github repo in README.md)
		// and the upstream /api submodule import path by temporarily masking
		// them. The replacement is a plain substring swap with no module-path
		// boundary, so without the api mask "<oldMod>/api/gen/..." would become
		// "<newMod>/api/gen/..." and the project would compile its own second
		// copy of the annotations descriptor. The mask token must be absent from
		// the file — a fixed literal would itself be rewritten in any file that
		// contains it (e.g. this very source), so derive a unique one per file;
		// suffixing one unique token keeps both masks unique.
		mask := uniqueMask(s)
		urlMask, apiMask := mask+"A", mask+"B"
		s = strings.ReplaceAll(s, "https://"+oldMod, urlMask)
		s = apiRe.ReplaceAllString(s, apiMask+"${1}")
		s = strings.ReplaceAll(s, oldMod, newMod)
		s = strings.ReplaceAll(s, apiMask, apiMod)
		s = strings.ReplaceAll(s, urlMask, "https://"+oldMod)
		return s
	})
}

// namespaceSample moves the layout's sample service into the project's own proto
// namespace. Every project scaffolded from gortexa used to declare resource.v1 at
// resource/v1/resource.proto; protobuf's global registry is keyed on exactly that
// package and that file path, so no two of them could be linked into one binary,
// and neither could a project and anything else carrying the layout's sample. The
// physical move keeps buf's PACKAGE_DIRECTORY_MATCH satisfied. This runs after
// gen/ has been pruned, so there is no length-prefixed descriptor to corrupt —
// the project regenerates all of gen/ from the moved proto.
func namespaceSample(root, ns string) error {
	from := filepath.Join(root, "proto", "resource")
	if _, err := os.Stat(from); err == nil {
		to := filepath.Join(root, "proto", ns, "resource")
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return rewriteTextFiles(root, func(s string) string {
		if !strings.Contains(s, "resource") {
			return s
		}
		// Paths first (proto dir, gen dir, Go import paths), then the proto
		// package and its full names, then the descriptor variable protoc-gen-go
		// derives from the file path. None of the three overlaps the others.
		s = strings.ReplaceAll(s, "resource/v1", ns+"/resource/v1")
		s = strings.ReplaceAll(s, "resource.v1", ns+".resource.v1")
		s = strings.ReplaceAll(s, "File_resource_v1_resource_proto", "File_"+ns+"_resource_v1_resource_proto")
		return s
	})
}

// rewriteTextFiles applies fn to the contents of every regular text file under
// root, skipping .git, symlinks (the .claude/.codex/.github/.agents skill links),
// devices and oversized or binary files.
func rewriteTextFiles(root string, fn func(string) string) error {
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
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil //nolint:nilerr // skipping a non-regular/unreadable file is not fatal
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(b) {
			return nil
		}
		out := fn(string(b))
		if out == string(b) {
			return nil
		}
		return os.WriteFile(path, []byte(out), info.Mode().Perm())
	})
}

// devPlaceholderSecret mirrors config: the value the server refuses to
// boot with. `create` swaps it for a fresh random secret in the new project.

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
make run         # dev server on :8080 (injects a local-only dev secret)
make test
`+"```"+`

## Before it will start

`+"`etc/config.yaml`"+` ships a placeholder JWT secret that the server refuses to
boot with, so the signing key can never arrive by way of a committed file.
Inject a real one (>= 32 bytes) from your secret manager, a mounted file or the
environment:

`+"```bash"+`
export GORTEXA_AUTH__JWT_SECRET="$(openssl rand -hex 32)"
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

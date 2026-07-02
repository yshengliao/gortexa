package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newGenCmd() *cobra.Command {
	var noWire, skipGen, force, allowBreaking bool
	cmd := &cobra.Command{
		Use:   "gen <domain>/<version> [Entity]",
		Short: "Generate a new CRUD API end-to-end: proto + logic stub + server wiring, then regenerate",
		Long: "gen writes a proto contract (validation + AI-tool + HTTP annotations) and an\n" +
			"in-memory logic stub, wires the service into cmd/server/main.go, and runs the\n" +
			"buf pipeline — so one command yields a working gRPC + HTTP/JSON + MCP API.\n\n" +
			"Example: gortexa gen billing/v1 Invoice",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			entity := ""
			if len(args) == 2 {
				entity = args[1]
			}
			data, err := parseTarget(args[0], entity)
			if err != nil {
				return err
			}
			root, module, err := findModuleRoot(mustGetwd())
			if err != nil {
				return err
			}
			data.Module = module
			return generateAPI(root, data, genOpts{noWire: noWire, skipGen: skipGen, force: force, allowBreaking: allowBreaking})
		},
	}
	cmd.Flags().BoolVar(&noWire, "no-wire", false, "do not wire the service into cmd/server/main.go")
	cmd.Flags().BoolVar(&skipGen, "skip-gen", false, "do not run code generation (buf) afterwards")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing proto/logic files")
	cmd.Flags().BoolVar(&allowBreaking, "allow-breaking", false, "skip the buf breaking-change gate during regeneration (implied by --force)")
	return cmd
}

type genOpts struct{ noWire, skipGen, force, allowBreaking bool }

func generateAPI(root string, d tmplData, opt genOpts) error {
	protoPath := filepath.Join(root, "proto", d.Domain, d.Version, d.Snake+".proto")
	logicPath := filepath.Join(root, "internal", "logic", d.Snake+".go")
	mainPath := filepath.Join(root, "cmd", "server", "main.go")

	// Fail before writing any artifact so a rejected generation does not leave the
	// project half-created (for example proto written but logic already existed, or
	// proto/logic written before discovering that cmd/server/main.go is not wireable).
	if err := preflightGenerate(mainPath, d, opt); err != nil {
		return err
	}
	for _, path := range []string{protoPath, logicPath} {
		if err := ensureWritable(path, opt.force); err != nil {
			return err
		}
	}

	if err := writeRendered(protoPath, "proto.tmpl", d); err != nil {
		return err
	}
	fmt.Println("  + " + rel(root, protoPath))
	if err := writeRendered(logicPath, "logic.tmpl", d); err != nil {
		return err
	}
	fmt.Println("  + " + rel(root, logicPath))

	if !opt.noWire {
		if err := wireServer(mainPath, d); err != nil {
			return fmt.Errorf("wire server: %w", err)
		}
		fmt.Println("  ~ " + rel(root, mainPath) + " (wired)")
	}

	if !opt.skipGen {
		fmt.Println("==> regenerating (buf lint → breaking → generate)…")
		// --force overwrites a committed API, which would otherwise trip the
		// breaking gate on its own new files; imply --allow-breaking there.
		if err := regen(root, opt.allowBreaking || opt.force); err != nil {
			return fmt.Errorf("regen: %w", err)
		}
		// gofmt the new logic file and the rewired main.go now that the generated
		// types exist.
		_ = runCmd(root, "gofmt", "-w", logicPath, mainPath)
	}
	fmt.Printf("\n==> %s.%s.%sService ready — build with `go build ./...`\n", d.Domain, d.Version, d.Entity)
	return nil
}

func preflightGenerate(mainPath string, d tmplData, opt genOpts) error {
	if opt.noWire {
		return nil
	}
	if err := validateWireable(mainPath, d); err != nil {
		return fmt.Errorf("wire server: %w", err)
	}
	return nil
}

func ensureWritable(path string, force bool) error {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%q is a directory", path)
		}
		if !force {
			return fmt.Errorf("%q already exists (use --force to overwrite)", path)
		}
		return nil
	}
	// A stat error other than "not found" (e.g. a permission error) means we
	// cannot safely write here; fail in preflight rather than mid-generation.
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeRendered(path, tmpl string, d tmplData) error {
	out, err := renderTemplate(tmpl, d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}

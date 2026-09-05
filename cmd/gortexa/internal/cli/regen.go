package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newRegenCmd() *cobra.Command {
	var allowBreaking bool
	cmd := &cobra.Command{
		Use:   "regen",
		Short: "Regenerate code from proto (buf lint → breaking → generate)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			root, _, err := findModuleRoot(mustGetwd())
			if err != nil {
				return err
			}
			return regen(root, allowBreaking)
		},
	}
	cmd.Flags().BoolVar(&allowBreaking, "allow-breaking", false, "skip the buf breaking-change gate (intended break)")
	return cmd
}

// regen runs the deterministic proto pipeline — buf lint → breaking → generate —
// mirroring the proto-regen skill. The breaking gate is checked against the local
// default branch when it exists, and skipped when --allow-breaking is set.
func regen(root string, allowBreaking bool) error {
	if _, err := exec.LookPath("buf"); err != nil {
		return fmt.Errorf("buf not found in PATH — run `gortexa tools install` (or `make bootstrap`) first")
	}
	if err := runCmd(root, "buf", "lint"); err != nil {
		return fmt.Errorf("buf lint: %w", err)
	}
	if !allowBreaking && isGitToplevel(root) && gitRefExists(root, "main") {
		if err := runCmd(root, "buf", "breaking", "--against", ".git#branch=main"); err != nil {
			return fmt.Errorf("buf breaking: %w — pass --allow-breaking if the change is intended", err)
		}
	}
	// buf's `out` is per-plugin, not per-module, so one invocation cannot split
	// output across the two module trees; api/ generates with its own template.
	// A scaffolded project has no api/buf.gen.yaml — it depends on the published
	// api module instead of regenerating the annotations — so this step is
	// skipped there and only the project's own protos are generated.
	if fileExists(filepath.Join(root, "api", "buf.gen.yaml")) {
		if err := runCmd(root, "buf", "generate", "--template", "api/buf.gen.yaml", "--path", "proto/gortexa"); err != nil {
			return fmt.Errorf("buf generate (api): %w", err)
		}
	}
	if err := runCmd(root, "buf", "generate", "--exclude-path", "proto/gortexa"); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	return nil
}

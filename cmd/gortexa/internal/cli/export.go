package cli

import (
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"
)

// newExportCmd shells into the project like `run` does: the schemas are
// rendered by the project's own cmd/server binary, which links the generated
// proto packages. Doing it in-process here would make this CLI depend on gen/
// output and break plain `go install` of cmd/gortexa.
func newExportCmd() *cobra.Command {
	var format, out string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the project's ai.v1 tool schemas (mcp | openai | gemini)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !slices.Contains([]string{"mcp", "openai", "gemini"}, format) {
				return fmt.Errorf("unknown --format %q (want mcp, openai or gemini)", format)
			}
			root, _, err := findModuleRoot(mustGetwd())
			if err != nil {
				return err
			}
			b, err := runCmdOutput(root, "go", "run", "./cmd/server", "-export-ai-schemas="+format)
			if err != nil {
				return fmt.Errorf("export schemas: %w", err)
			}
			if out == "" {
				_, err = os.Stdout.Write(b)
				return err
			}
			return os.WriteFile(out, b, 0o644)
		},
	}
	cmd.Flags().StringVar(&format, "format", "mcp", "schema flavour: mcp | openai | gemini")
	cmd.Flags().StringVar(&out, "out", "", "write to a file instead of stdout")
	return cmd
}

// Package cli implements the gortexa developer CLI: scaffold projects, generate
// APIs from proto, regenerate code, and manage the dev toolchain and AI skills.
package cli

import "github.com/spf13/cobra"

// version is overridable via -ldflags "-X ...cli.version=x.y.z".
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gortexa",
		Short:         "Developer CLI for the Gortexa contract-first gRPC framework",
		Long:          "gortexa scaffolds projects and APIs for the Gortexa framework:\nProtobuf is the single source of truth; one h2c port serves gRPC + HTTP/JSON + MCP.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newCreateCmd(),
		newGenCmd(),
		newRegenCmd(),
		newRunCmd(),
		newExportCmd(),
		newToolsCmd(),
		newSkillsCmd(),
		newDoctorCmd(),
		newVersionCmd(),
	)
	return root
}

// Execute runs the root command, returning any error to main.
func Execute() error {
	return newRootCmd().Execute()
}

// Package cli implements the gortexa developer CLI: scaffold projects, generate
// APIs from proto, regenerate code, and manage the dev toolchain and AI skills.
package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridable via -ldflags "-X ...cli.version=x.y.z". Nothing in the
// documented install path sets it, so on its own it is always "dev"; cliVersion
// is what the CLI reports.
var version = "dev"

// cliVersion is the version the CLI prints and records in a project's manifest.
// An explicit -ldflags value wins. Otherwise the module version stamped by the
// Go toolchain is used: `go install .../cmd/gortexa@v1.2.3` records v1.2.3 in
// the binary's build info — the only version the documented install path ever
// produces — and a build inside a checkout gets a version derived from the VCS
// state (e.g. v1.2.3+dirty, or a pseudo-version). Only a build with no module
// version at all ("(devel)": no VCS, or -buildvcs=false) falls through to "dev",
// suffixed with the revision when one was stamped anyway.
func cliVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return "dev-" + s.Value[:7]
		}
	}
	return version
}

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

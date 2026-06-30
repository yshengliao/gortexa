package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "run [-- args...]",
		Short:              "Build and run the dev server (go run ./cmd/server)",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			root, _, err := findModuleRoot(mustGetwd())
			if err != nil {
				return err
			}
			return runCmd(root, "go", append([]string{"run", "./cmd/server"}, args...)...)
		},
	}
}

func newToolsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tools", Short: "Manage the pinned dev toolchain (tools/go.mod directives)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install all pinned tools into GOBIN (go -C tools install tool)",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				root, _, err := findModuleRoot(mustGetwd())
				if err != nil {
					return err
				}
				return runCmd(root, "go", "-C", "tools", "install", "tool")
			},
		},
		&cobra.Command{
			Use:   "sync [tool@version ...]",
			Short: "Re-pin or upgrade tool directives (go -C tools get -tool …)",
			RunE: func(_ *cobra.Command, args []string) error {
				root, _, err := findModuleRoot(mustGetwd())
				if err != nil {
					return err
				}
				if len(args) == 0 {
					return runCmd(root, "go", "-C", "tools", "get", "tool")
				}
				for _, a := range args {
					if err := runCmd(root, "go", "-C", "tools", "get", "-tool", a); err != nil {
						return err
					}
				}
				return nil
			},
		},
	)
	return cmd
}

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "skills", Short: "Manage AI-assist skills (.skills/*)"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Wire .skills/* into Claude Code, Codex, Copilot and Antigravity",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				root, _, err := findModuleRoot(mustGetwd())
				if err != nil {
					return err
				}
				return runCmd(root, "bash", "install-skills.sh")
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List bundled skills",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				root, _, err := findModuleRoot(mustGetwd())
				if err != nil {
					return err
				}
				entries, err := os.ReadDir(filepath.Join(root, ".skills"))
				if err != nil {
					return err
				}
				for _, e := range entries {
					if e.IsDir() {
						fmt.Println("  " + e.Name())
					}
				}
				return nil
			},
		},
	)
	return cmd
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the dev environment (Go toolchain, proto tools on PATH)",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return doctor() },
	}
}

func doctor() error {
	ok := true
	if out, err := exec.Command("go", "version").Output(); err == nil {
		fmt.Printf("  [ok]      go: %s", string(out))
	} else {
		ok = false
		fmt.Println("  [MISSING] go (install Go >= 1.26 from https://go.dev/dl/)")
	}
	for _, t := range []string{"buf", "protoc-gen-go", "protoc-gen-go-grpc", "protoc-gen-grpc-gateway", "protoc-gen-openapiv2", "sqlc"} {
		if _, err := exec.LookPath(t); err == nil {
			fmt.Printf("  [ok]      %s\n", t)
		} else {
			ok = false
			fmt.Printf("  [MISSING] %s\n", t)
		}
	}
	if !ok {
		return fmt.Errorf("environment incomplete — run `gortexa tools install`")
	}
	fmt.Println("environment OK")
	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the gortexa CLI version",
		Args:  cobra.NoArgs,
		Run:   func(_ *cobra.Command, _ []string) { fmt.Println("gortexa", version) },
	}
}

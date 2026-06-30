// Command gortexa is the Gortexa developer CLI. It scaffolds new projects,
// generates APIs (proto + logic + wiring) from one command, regenerates code,
// and manages the pinned dev toolchain and AI-assist skills.
package main

import (
	"fmt"
	"os"

	"github.com/yshengliao/gortexa/cmd/gortexa/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gortexa:", err)
		os.Exit(1)
	}
}

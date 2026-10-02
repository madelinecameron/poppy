// Command poppy provides fleet-wide git awareness across personal machines.
package main

import (
	"context"
	"os"

	"github.com/madelinecameron/poppy/internal/agent"
	"github.com/madelinecameron/poppy/internal/cli"
)

// Version and version are set at build time with
// -ldflags "-X main.Version=$v -X main.version=$v". Go silently ignores an -X
// for a symbol that does not exist, so both spellings are declared.
var (
	Version string
	version string
)

func main() {
	if Version != "" {
		agent.SetVersion(Version)
	} else {
		agent.SetVersion(version)
	}
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}

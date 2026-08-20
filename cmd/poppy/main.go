// Command poppy provides fleet-wide git awareness across personal machines.
package main

import (
	"context"
	"os"

	"github.com/madelinecameron/poppy/internal/agent"
	"github.com/madelinecameron/poppy/internal/cli"
)

// Version and version are both stamped by the closet publisher, which passes
// -ldflags "-X main.Version=$v -X main.version=$v". Go silently discards an -X
// for a symbol that does not exist, so before these were declared the stamp was
// dropped and every published build called itself "dev". Both spellings are
// declared because the publisher sets both.
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

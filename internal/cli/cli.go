// Package cli implements poppy's command line.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/hub"
)

const usage = `poppy — fleet-wide git awareness

usage: poppy <command> [flags]

commands:
  status      fleet overview: machines, dirty trees, unpushed work
  dirty       every dirty working tree in the fleet, oldest first
  find        locate a repository or branch across machines
  machines    agents, versions, and heartbeats
  scan        run one scan of this machine now
  daemon      start the background agent (--stop, --status, --install)

  snapshot    capture this repository's uncommitted work now
  timeline    list snapshots recorded for a repository
  restore     restore a snapshot into the working tree
  grab        teleport uncommitted work from another machine

  mcp         serve fleet state to agents over the Model Context Protocol
  init        write a starter configuration file
  version     print the agent version

run "poppy <command> -h" for command flags.
`

// Main dispatches a command and returns a process exit code.
func Main(ctx context.Context, args []string) int {
	if len(args) < 1 {
		fmt.Print(usage)
		return 2
	}

	cmd, rest := args[0], args[1:]
	var err error

	switch cmd {
	case "status":
		err = runStatus(ctx, rest)
	case "dirty":
		err = runDirty(ctx, rest)
	case "find":
		err = runFind(ctx, rest)
	case "machines":
		err = runMachines(ctx, rest)
	case "scan":
		err = runScan(ctx, rest)
	case "snapshot":
		err = runSnapshot(ctx, rest)
	case "timeline":
		err = runTimeline(ctx, rest)
	case "restore":
		err = runRestore(ctx, rest)
	case "grab":
		err = runGrab(ctx, rest)
	case "daemon":
		err = runDaemon(ctx, rest)
	case "mcp":
		err = runMCP(ctx, rest)
	case "init":
		err = runInit(ctx, rest)
	case "version":
		err = runVersion(ctx, rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "poppy: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "poppy: %v\n", err)
		return 1
	}
	return 0
}

// setup loads configuration and opens the local hub store.
func setup(ctx context.Context) (config.Config, *hub.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	store, err := hub.Open(ctx, filepath.Join(config.DataDir(), "hub"), cfg.HubRemote)
	if err != nil {
		return cfg, nil, err
	}
	return cfg, store, nil
}

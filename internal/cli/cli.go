// Package cli implements poppy's command line.
package cli

import (
	"context"
	"errors"
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

  secrets     credentials found across the fleet, including in stashes and
              unpushed commits — content no remote has ever seen
  hooks       install / verify the pre-commit secret hook across repositories

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
	case "secrets":
		err = runSecrets(ctx, rest)
	case "hooks":
		err = runHooks(ctx, rest)
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

	var code exitCode
	switch {
	case errors.As(err, &code):
		return int(code)
	case err != nil:
		fmt.Fprintf(os.Stderr, "poppy: %v\n", err)
		return 1
	}
	return 0
}

// exitCode ends a command with a non-zero status without a further message,
// for one that has already said everything there is to say. The pre-commit
// hook path uses it: git shows the command's own output and nothing else is
// wanted above it.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// loadConfigOnly reads configuration without opening the hub, for commands
// that act on the local machine alone. The pre-commit hook runs on every
// commit and has no business touching the shared state repository.
func loadConfigOnly() (config.Config, error) { return config.Load() }

// setup loads configuration and opens the local hub store for writing,
// creating it when absent. Only the commands that record state need this.
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

// setupRead loads configuration and opens the hub for reading. Reading has no
// business creating the shared state repository or pinning an identity in it;
// those are the publish path's needs, and making a reader satisfy them means
// every read fails on an account that cannot write. It also means a machine
// that only ever asked a question ends up with a git repository it never used.
//
// A hub that is not there is reported rather than treated as an empty fleet,
// because the two are different answers and only one of them is a number.
func setupRead() (config.Config, *hub.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	dir := filepath.Join(config.DataDir(), "hub")
	store := hub.OpenRead(dir, cfg.HubRemote)
	if !store.Exists() {
		return cfg, nil, fmt.Errorf("no hub at %s — run \"poppy scan\" to record this machine", dir)
	}
	return cfg, store, nil
}

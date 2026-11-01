package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/hub"
	"github.com/madelinecameron/poppy/internal/model"
)

func runMachines(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "retire" {
		return runMachinesRetire(ctx, args[1:])
	}
	fs := flag.NewFlagSet("machines", flag.ContinueOnError)
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setupRead(ctx)
	if err != nil {
		return err
	}
	if !*noPull {
		store.Pull(ctx)
	}

	fleet, err := store.Fleet()
	if err != nil {
		return err
	}
	if len(fleet.Machines) == 0 {
		fmt.Println("No machines have reported yet.")
		return nil
	}

	now := time.Now()
	staleAfter := time.Duration(cfg.StaleAfterSeconds) * time.Second

	var t table
	t.setHeader("MACHINE", "SEEN", "REPOS", "PLATFORM", "AGENT", "SCAN")
	for _, m := range fleet.Machines {
		name := m.Manifest.Machine
		if name == cfg.Machine {
			name += paint(dim, " (this)")
		}
		seen := relTime(m.Manifest.Heartbeat, now)
		if m.Stale(now, staleAfter) {
			seen = paint(yellow, seen)
		} else {
			seen = paint(green, seen)
		}
		t.add(
			paint(cyan, name),
			seen,
			fmt.Sprint(len(m.Repos)),
			paint(dim, m.Manifest.OS+"/"+m.Manifest.Arch),
			paint(dim, m.Manifest.Version),
			paint(dim, (time.Duration(m.Manifest.ScanMillis)*time.Millisecond).String()),
		)
	}
	t.render(os.Stdout, "")
	return nil
}

// runMachinesRetire removes a dead machine's records from the hub. It refuses
// this machine, and a machine whose heartbeat is not yet stale unless forced:
// a live poppy would write its records straight back.
func runMachinesRetire(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("machines retire", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be removed and change nothing")
	force := fs.Bool("force", false, "retire a machine whose heartbeat is not yet stale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: poppy machines retire [--dry-run] [--force] <machine>")
	}
	name := fs.Arg(0)

	var (
		cfg   config.Config
		store *hub.Store
		err   error
	)
	if *dryRun {
		cfg, store, err = setupRead(ctx)
	} else {
		cfg, store, err = setup(ctx)
	}
	if err != nil {
		return err
	}
	if err := store.Pull(ctx); err != nil {
		return fmt.Errorf("fetch the hub before retiring %s: %w", name, err)
	}
	fleet, err := store.Fleet()
	if err != nil {
		return err
	}
	files, err := store.MachineFiles(name)
	if err != nil {
		return err
	}

	staleAfter := time.Duration(cfg.StaleAfterSeconds) * time.Second
	if err := checkRetire(name, cfg.Machine, fleet, len(files), time.Now(), staleAfter, *force); err != nil {
		return err
	}

	printRetire(os.Stdout, name, files, *dryRun)
	if *dryRun {
		return nil
	}
	pushed, err := store.Retire(ctx, name)
	if err != nil {
		return err
	}
	if !pushed && cfg.HubRemote != "" {
		return fmt.Errorf("retired %s locally but the push did not reach the hub; run it again", name)
	}
	fmt.Printf("retired %s\n", name)
	return nil
}

func checkRetire(name, thisMachine string, fleet model.Fleet, files int, now time.Time, staleAfter time.Duration, force bool) error {
	if strings.EqualFold(name, thisMachine) {
		return fmt.Errorf("%s is this machine; retire it from another one", name)
	}
	if files == 0 {
		return fmt.Errorf("the hub has no records for %s", name)
	}
	for _, m := range fleet.Machines {
		if m.Manifest.Machine == name && !m.Stale(now, staleAfter) && !force {
			return fmt.Errorf("%s reported %s; it looks alive and would publish again (use --force)",
				name, relTime(m.Manifest.Heartbeat, now))
		}
	}
	return nil
}

func printRetire(w io.Writer, name string, files []string, dryRun bool) {
	verb := "removing"
	if dryRun {
		verb = "would remove"
	}
	fmt.Fprintf(w, "%s %s from %s:\n", verb, plural(len(files), "file"), name)
	for _, f := range files {
		fmt.Fprintf(w, "  %s\n", f)
	}
}

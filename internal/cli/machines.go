package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func runMachines(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("machines", flag.ContinueOnError)
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setupRead()
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

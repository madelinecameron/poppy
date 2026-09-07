package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/grab"
	"github.com/madelinecameron/poppy/internal/model"
)

func runStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	all := fs.Bool("all", false, "include clean, fully pushed repositories")
	asJSON := fs.Bool("json", false, "emit the fleet document as JSON")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setupRead()
	if err != nil {
		return err
	}

	var pullErr error
	if !*noPull {
		pullErr = store.Pull(ctx)
	}

	fleet, err := store.Fleet()
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(fleet)
	}

	now := time.Now()
	printFleet(fleet, cfg.Machine, time.Duration(cfg.StaleAfterSeconds)*time.Second, now, *all)

	if diverged := grab.Divergences(fleet); len(diverged) > 0 {
		fmt.Println()
		fmt.Println(paint(bold+yellow, "DIVERGING") + "  same branch dirty on multiple machines:")
		for _, d := range diverged {
			fmt.Printf("  %s on %s — %s\n",
				paint(bold, d.Name),
				paint(cyan, d.Branch),
				strings.Join(d.Machines, ", "))
		}
		fmt.Println(paint(dim, "  commit or grab before these drift further apart"))
	}

	if len(fleet.Machines) == 0 {
		fmt.Println()
		fmt.Println(paint(dim, "No state yet. Run `poppy scan` to record this machine."))
	}
	if pullErr != nil {
		fmt.Println()
		fmt.Println(paint(dim, "hub unreachable; showing last known state"))
	}
	return nil
}

func printFleet(fleet model.Fleet, thisMachine string, staleAfter time.Duration, now time.Time, all bool) {
	var repos, dirty, unpushed, stashes, localOnly, broken, findings int
	for _, m := range fleet.Machines {
		repos += len(m.Repos)
		for _, r := range m.Repos {
			if r.Broken {
				broken++
				continue
			}
			if r.Dirty.Any() {
				dirty++
			}
			if r.Unpushed() {
				unpushed++
			}
			if r.LocalOnly() {
				localOnly++
			}
			stashes += r.Stashes
			findings += r.SecretCount()
		}
	}

	summary := []string{
		plural(len(fleet.Machines), "machine"),
		plural(repos, "repo"),
	}
	if dirty > 0 {
		summary = append(summary, paint(yellow, fmt.Sprintf("%d dirty", dirty)))
	}
	if unpushed > 0 {
		summary = append(summary, paint(red, fmt.Sprintf("%d unpushed", unpushed)))
	}
	if stashes > 0 {
		summary = append(summary, fmt.Sprintf("%d stashed", stashes))
	}
	if localOnly > 0 {
		summary = append(summary, fmt.Sprintf("%d no remote", localOnly))
	}
	if broken > 0 {
		summary = append(summary, paint(red, fmt.Sprintf("%d unreadable", broken)))
	}
	if findings > 0 {
		summary = append(summary, paint(bold+red, plural(findings, "secret")))
	}
	fmt.Printf("%s  %s\n", paint(bold, "FLEET"), strings.Join(summary, " · "))

	// Directly under the fleet counts, because a hub nobody can write to
	// makes every number above it a local one.
	printHubIntegrity(os.Stdout, fleet.HubIntegrity(), now)

	for _, m := range fleet.Machines {
		fmt.Println()
		printMachine(m, thisMachine, staleAfter, now, all)
	}
}

func printMachine(m model.MachineState, thisMachine string, staleAfter time.Duration, now time.Time, all bool) {
	name := paint(bold+cyan, m.Manifest.Machine)
	if m.Manifest.Machine == thisMachine {
		name += paint(dim, " (this machine)")
	}

	heartbeat := relTime(m.Manifest.Heartbeat, now)
	if m.Stale(now, staleAfter) {
		heartbeat = paint(yellow, "last seen "+heartbeat)
	} else {
		heartbeat = paint(green, "up") + paint(dim, " · "+heartbeat)
	}

	fmt.Printf("%s  %s  %s\n", name, heartbeat,
		paint(dim, fmt.Sprintf("%s · %s/%s", plural(len(m.Repos), "repo"), m.Manifest.OS, m.Manifest.Arch)))

	var t table
	shown := 0
	for _, r := range m.Repos {
		// A fully pushed, clean repository holding a committed credential is
		// not at risk of being lost, but it is exactly what needs looking at.
		if !r.AtRisk() && r.SecretCount() == 0 && !all {
			continue
		}
		shown++
		t.add(repoRow(r, now)...)
	}

	if t.empty() {
		fmt.Println(paint(dim, "  all clean"))
		return
	}
	t.render(os.Stdout, "  ")

	if hidden := len(m.Repos) - shown; hidden > 0 && !all {
		fmt.Println(paint(dim, fmt.Sprintf("  %s clean (use --all)", plural(hidden, "repo"))))
	}
}

func repoRow(r model.Repo, now time.Time) []string {
	if r.Broken {
		return []string{r.Name, paint(dim, "-"), paint(red, "unreadable"), paint(dim, r.Error)}
	}

	branch := r.Head.Branch
	switch {
	case r.Head.Unborn:
		branch = "(unborn)"
	case r.Head.Detached:
		branch = "(detached)"
	}

	var marks []string
	if head, ok := headBranch(r); ok {
		switch {
		case r.HasRemote() && head.Upstream == "":
			marks = append(marks, paint(red, "never pushed"))
		case head.Gone:
			marks = append(marks, paint(red, "upstream gone"))
		}
		if head.Ahead > 0 {
			marks = append(marks, paint(red, fmt.Sprintf("↑%d", head.Ahead)))
		}
		if head.Behind > 0 {
			marks = append(marks, paint(blue, fmt.Sprintf("↓%d", head.Behind)))
		}
	}
	if n := otherUnpushedBranches(r); n > 0 {
		marks = append(marks, paint(red, plural(n, "unpushed branch")))
	}
	if r.Stashes > 0 {
		marks = append(marks, fmt.Sprintf("%d stash", r.Stashes))
	}
	if r.LocalOnly() {
		marks = append(marks, paint(yellow, "no remote"))
	}
	if r.Snapshot != nil && r.Snapshot.Synced {
		marks = append(marks, paint(green, "synced"))
	}
	if n := r.SecretCount(); n > 0 {
		marks = append(marks, paint(bold+red, plural(n, "secret")))
	}

	dirtyCell := paint(dim, "clean")
	if r.Dirty.Any() {
		age := ""
		if r.DirtySince != nil {
			age = " " + paint(ageColor(*r.DirtySince, now), shortAge(*r.DirtySince, now))
		}
		dirtyCell = paint(yellow, fmt.Sprintf("%s dirty", plural(r.Dirty.Total(), "file"))) + age
	}

	name := r.Name
	if r.IsWorktree {
		name += paint(dim, " (wt)")
	}

	return []string{name, paint(dim, branch), dirtyCell, strings.Join(marks, " ")}
}

func headBranch(r model.Repo) (model.Branch, bool) {
	for _, b := range r.Branches {
		if b.Name == r.Head.Branch {
			return b, true
		}
	}
	return model.Branch{}, false
}

// otherUnpushedBranches counts unpushed branches besides HEAD, whose own
// tracking state is already shown in its own column.
func otherUnpushedBranches(r model.Repo) int {
	n := 0
	for _, b := range r.UnpushedBranches() {
		if b.Name != r.Head.Branch {
			n++
		}
	}
	return n
}

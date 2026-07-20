package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mads/poppy/internal/model"
)

func runDirty(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dirty", flag.ContinueOnError)
	files := fs.Bool("files", false, "list the changed paths for each repository")
	minAge := fs.Duration("min-age", 0, "only show work dirty for at least this long")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, store, err := setup(ctx)
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

	now := time.Now()
	type entry struct {
		machine string
		repo    model.Repo
	}
	var entries []entry
	for _, m := range fleet.Machines {
		for _, r := range m.Repos {
			if !r.Dirty.Any() {
				continue
			}
			if *minAge > 0 && (r.DirtySince == nil || now.Sub(*r.DirtySince) < *minAge) {
				continue
			}
			entries = append(entries, entry{m.Manifest.Machine, r})
		}
	}

	// Oldest first: the longer work has sat uncommitted, the more it matters.
	sort.Slice(entries, func(i, j int) bool {
		return dirtyStart(entries[i].repo).Before(dirtyStart(entries[j].repo))
	})

	if len(entries) == 0 {
		fmt.Println(paint(green, "No dirty trees anywhere in the fleet."))
		return nil
	}

	var t table
	t.setHeader("AGE", "MACHINE", "REPO", "BRANCH", "CHANGES")
	for _, e := range entries {
		start := dirtyStart(e.repo)
		branch := e.repo.Head.Branch
		if branch == "" {
			branch = "(detached)"
		}
		t.add(
			paint(ageColor(start, now), shortAge(start, now)),
			paint(cyan, e.machine),
			e.repo.Name,
			branch,
			describeDirty(e.repo.Dirty),
		)
	}
	t.render(os.Stdout, "")

	if *files {
		for _, e := range entries {
			fmt.Println()
			fmt.Printf("%s %s\n", paint(cyan, e.machine+":"+e.repo.Name), paint(dim, e.repo.Path))
			for _, f := range e.repo.Dirty.Files {
				fmt.Println("  " + f)
			}
			if e.repo.Dirty.Truncated {
				fmt.Println(paint(dim, "  … list truncated"))
			}
		}
	}
	return nil
}

func dirtyStart(r model.Repo) time.Time {
	if r.DirtySince != nil {
		return *r.DirtySince
	}
	return r.UpdatedAt
}

func describeDirty(d model.Dirty) string {
	var parts []string
	if d.Staged > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", d.Staged))
	}
	if d.Unstaged > 0 {
		parts = append(parts, fmt.Sprintf("%d unstaged", d.Unstaged))
	}
	if d.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", d.Untracked))
	}
	if d.Unmerged > 0 {
		parts = append(parts, paint(red, fmt.Sprintf("%d unmerged", d.Unmerged)))
	}
	return strings.Join(parts, ", ")
}

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/model"
)

func runFind(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := strings.ToLower(strings.Join(fs.Args(), " "))
	if query == "" {
		return errors.New("find: give a repository or branch name to look for")
	}

	_, store, err := setupRead()
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
	type hit struct {
		machine  string
		repo     model.Repo
		branches []model.Branch
		score    int
	}
	var hits []hit

	for _, m := range fleet.Machines {
		for _, r := range m.Repos {
			h := hit{machine: m.Manifest.Machine, repo: r}

			if s := matchScore(r.Name, query); s > 0 {
				h.score = s + 10
			} else if strings.Contains(strings.ToLower(r.Path), query) {
				h.score = 1
			}
			for _, remote := range r.Remotes {
				if strings.Contains(strings.ToLower(remote), query) && h.score < 5 {
					h.score = 5
				}
			}
			for _, b := range r.Branches {
				if s := matchScore(b.Name, query); s > 0 {
					h.branches = append(h.branches, b)
					if s+10 > h.score {
						h.score = s + 10
					}
				}
			}
			if h.score > 0 {
				hits = append(hits, h)
			}
		}
	}

	if len(hits) == 0 {
		fmt.Printf("No repository or branch matching %q.\n", query)
		return nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].machine < hits[j].machine
	})

	for i, h := range hits {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s %s\n", paint(bold+cyan, h.machine+":"+h.repo.Name), paint(dim, h.repo.Path))

		var t table
		state := paint(dim, "clean")
		if h.repo.Dirty.Any() {
			age := ""
			if h.repo.DirtySince != nil {
				age = " " + paint(ageColor(*h.repo.DirtySince, now), shortAge(*h.repo.DirtySince, now))
			}
			state = paint(yellow, describeDirty(h.repo.Dirty)) + age
		}
		t.add("HEAD", paint(dim, headLabel(h.repo)), state)

		for _, b := range h.branches {
			if b.Name == h.repo.Head.Branch {
				continue
			}
			t.add("branch", paint(dim, b.Name), trackLabel(b))
		}
		t.render(os.Stdout, "  ")
	}
	return nil
}

// matchScore ranks an exact name above a prefix above a substring, so the
// obvious answer to "where is this branch" surfaces first.
func matchScore(name, query string) int {
	n := strings.ToLower(name)
	switch {
	case n == query:
		return 3
	case strings.HasPrefix(n, query):
		return 2
	case strings.Contains(n, query):
		return 1
	}
	return 0
}

func headLabel(r model.Repo) string {
	switch {
	case r.Head.Unborn:
		return "(unborn)"
	case r.Head.Detached:
		return "(detached)"
	}
	return r.Head.Branch
}

func trackLabel(b model.Branch) string {
	switch {
	case b.Gone:
		return paint(red, "upstream gone")
	case b.Upstream == "":
		return paint(red, "no upstream")
	case b.Ahead > 0 && b.Behind > 0:
		return paint(red, fmt.Sprintf("↑%d ↓%d", b.Ahead, b.Behind))
	case b.Ahead > 0:
		return paint(red, fmt.Sprintf("↑%d", b.Ahead))
	case b.Behind > 0:
		return paint(blue, fmt.Sprintf("↓%d", b.Behind))
	}
	return paint(dim, "in sync")
}

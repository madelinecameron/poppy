package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/secrets"
	"github.com/madelinecameron/poppy/internal/secretscan"
)

func runSecrets(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("secrets", flag.ContinueOnError)
	staged := fs.Bool("staged", false, "scan the content staged in this repository and exit non-zero on a finding")
	asJSON := fs.Bool("json", false, "emit findings as JSON")
	rule := fs.String("rule", "", "only show findings from this rule")
	origin := fs.String("origin", "", "only show findings from worktree, stash, or commit")
	machine := fs.String("machine", "", "only show findings from this machine")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *staged {
		return runStagedScan(ctx)
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

	entries := collectFindings(fleet, *machine, *rule, *origin)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	printFindings(entries, cfg.Secrets.Enabled)
	return nil
}

// runStagedScan is what the pre-commit hook runs. It scans the index of the
// repository the commit is being made in and refuses the commit on a finding.
func runStagedScan(ctx context.Context) error {
	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	report := secretscan.Staged(ctx, dir, cfg.Secrets.ScanOptions())
	if report.Error != "" {
		fmt.Fprintf(os.Stderr, "poppy: staged scan degraded: %s\n", report.Error)
	}
	if !report.Any() {
		return nil
	}

	fmt.Fprintf(os.Stderr, "%s %s\n",
		paint(bold+red, "BLOCKED"),
		plural(report.Count(), "credential")+" found in the staged changes:")
	for _, f := range report.Findings {
		fmt.Fprintf(os.Stderr, "  %s  %s\n", paint(red, string(f.Rule())), f.Where())
	}
	fmt.Fprintln(os.Stderr, paint(dim, "\nRemove them, or commit with --no-verify if this is a false positive."))
	return exitCode(1)
}

// findingEntry is one finding placed in the fleet.
type findingEntry struct {
	Machine string `json:"machine"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	RepoDir string `json:"repoPath"`
	Rule    string `json:"rule"`
	Origin  string `json:"origin"`
	Ref     string `json:"ref,omitempty"`
	Line    int    `json:"line"`
}

func collectFindings(fleet model.Fleet, machine, rule, origin string) []findingEntry {
	entries := []findingEntry{}
	for _, m := range fleet.Machines {
		if machine != "" && m.Manifest.Machine != machine {
			continue
		}
		for _, r := range m.Repos {
			for _, f := range r.Secrets.All() {
				if rule != "" && string(f.Rule()) != rule {
					continue
				}
				if origin != "" && string(f.Origin()) != origin {
					continue
				}
				entries = append(entries, findingEntry{
					Machine: m.Manifest.Machine,
					Repo:    r.Name,
					RepoDir: r.Path,
					Path:    f.Path(),
					Rule:    string(f.Rule()),
					Origin:  string(f.Origin()),
					Ref:     f.Ref(),
					Line:    f.Line(),
				})
			}
		}
	}

	// Content that exists in exactly one place first: a credential in a stash
	// or an unpushed commit is invisible from every other machine and is the
	// reason this runs in poppy rather than in a hook.
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if ra, rb := originRank(a.Origin), originRank(b.Origin); ra != rb {
			return ra < rb
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Path < b.Path
	})
	return entries
}

func originRank(o string) int {
	switch secrets.Origin(o) {
	case secrets.OriginStash:
		return 0
	case secrets.OriginCommit:
		return 1
	}
	return 2
}

func printFindings(entries []findingEntry, enabled bool) {
	if len(entries) == 0 {
		if !enabled {
			fmt.Println(paint(yellow, "Secret scanning is disabled in this machine's configuration."))
			return
		}
		fmt.Println(paint(green, "No credentials found anywhere in the fleet."))
		return
	}

	unreachable := 0
	for _, e := range entries {
		if originRank(e.Origin) < 2 {
			unreachable++
		}
	}

	summary := []string{plural(len(entries), "finding")}
	if unreachable > 0 {
		summary = append(summary, paint(red, fmt.Sprintf("%d in content no remote has", unreachable)))
	}
	fmt.Printf("%s  %s\n\n", paint(bold+red, "SECRETS"), strings.Join(summary, " · "))

	var t table
	t.setHeader("WHERE", "MACHINE", "REPO", "RULE", "LOCATION")
	for _, e := range entries {
		t.add(
			paint(originColor(e.Origin), originLabel(e)),
			paint(cyan, e.Machine),
			e.Repo,
			paint(red, e.Rule),
			fmt.Sprintf("%s:%d", e.Path, e.Line),
		)
	}
	t.render(os.Stdout, "")

	if unreachable > 0 {
		fmt.Println()
		fmt.Println(paint(dim, "stash and unpushed findings exist on one machine only; a pre-commit hook cannot see them"))
	}
}

func originLabel(e findingEntry) string {
	switch secrets.Origin(e.Origin) {
	case secrets.OriginStash:
		return e.Ref
	case secrets.OriginCommit:
		if len(e.Ref) > 8 {
			return "commit " + e.Ref[:8]
		}
		return "commit"
	}
	return e.Origin
}

func originColor(o string) string {
	if originRank(o) < 2 {
		return red
	}
	return yellow
}

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/hooks"
	"github.com/madelinecameron/poppy/internal/scan"
)

func runHooks(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("hooks: give a subcommand — install, verify, or uninstall")
	}
	action, rest := args[0], args[1:]

	fs := flag.NewFlagSet("hooks "+action, flag.ContinueOnError)
	force := fs.Bool("force", false, "replace a pre-commit hook somebody else wrote, keeping a copy beside it")
	repo := fs.String("repo", "", "act on one repository instead of every configured one")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	targets, err := hookTargets(ctx, *repo)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return errors.New("hooks: no repositories found under the configured roots")
	}

	switch action {
	case "verify":
		return verifyHooks(ctx, targets)
	case "install":
		return installHooks(ctx, targets, *force)
	case "uninstall":
		return uninstallHooks(ctx, targets)
	}
	return fmt.Errorf("hooks: unknown subcommand %q", action)
}

// hookTargets lists the repositories to act on: one named repository, or every
// repository beneath the configured roots. That is the same
// set the agent scans, which is what makes "which of my repositories are unprotected" answerable.
func hookTargets(ctx context.Context, repo string) ([]string, error) {
	if repo != "" {
		return []string{scan.Expand(repo)}, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	found, err := scan.Run(ctx, scan.Options{
		Roots:    cfg.Roots,
		SkipDirs: cfg.SkipDirs,
		MaxDepth: cfg.MaxDepth,
		// Linked worktrees share their parent's hooks directory, so listing
		// them would install the same hook repeatedly and report it several
		// times over.
		IncludeWorktrees: false,
	})
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(found))
	for _, f := range found {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	return paths, nil
}

func verifyHooks(ctx context.Context, repos []string) error {
	statuses := make([]hooks.Status, 0, len(repos))
	counts := map[hooks.State]int{}
	for _, repo := range repos {
		st := hooks.Verify(ctx, repo)
		statuses = append(statuses, st)
		counts[st.State]++
	}

	fmt.Printf("%s  %s · %d protected · %s\n\n",
		paint(bold, "HOOKS"),
		plural(len(repos), "repo"),
		counts[hooks.StateInstalled]+counts[hooks.StateOutdated],
		paint(red, fmt.Sprintf("%d unprotected", counts[hooks.StateMissing]+counts[hooks.StateForeign])))

	var t table
	t.setHeader("STATE", "REPO")
	for _, st := range statuses {
		if st.State == hooks.StateInstalled {
			continue
		}
		t.add(paint(stateColor(st.State), string(st.State)), st.Repo)
	}
	if t.empty() {
		fmt.Println(paint(green, "every repository has poppy's pre-commit hook"))
		return nil
	}
	t.render(os.Stdout, "")

	if counts[hooks.StateForeign] > 0 {
		fmt.Println()
		fmt.Println(paint(dim, "foreign = a pre-commit hook somebody wrote; poppy will not replace one without --force"))
	}
	return nil
}

func installHooks(ctx context.Context, repos []string, force bool) error {
	var installed, skipped, blocked int
	var conflicts []string

	for _, repo := range repos {
		before := hooks.Verify(ctx, repo)
		st, err := hooks.Install(ctx, repo, force)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "  %s %s: %v\n", paint(red, "error"), repo, err)
		case st.State == hooks.StateForeign:
			blocked++
			conflicts = append(conflicts, repo)
		case before.State == hooks.StateInstalled:
			skipped++
		default:
			installed++
		}
	}

	fmt.Printf("%s  %d installed · %d already current · %s\n",
		paint(bold, "HOOKS"), installed, skipped,
		paint(map[bool]string{true: red, false: dim}[blocked > 0], fmt.Sprintf("%d left alone", blocked)))

	if blocked > 0 {
		fmt.Println()
		fmt.Println(paint(yellow, "these repositories already have a pre-commit hook somebody wrote:"))
		for _, repo := range conflicts {
			fmt.Println("  " + repo)
		}
		fmt.Println(paint(dim, "\nrerun with --force to replace them; the existing script is kept beside the new one"))
	}
	return nil
}

func uninstallHooks(ctx context.Context, repos []string) error {
	removed := 0
	for _, repo := range repos {
		before := hooks.Verify(ctx, repo)
		if !before.Protected() {
			continue
		}
		if _, err := hooks.Uninstall(ctx, repo); err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s: %v\n", paint(red, "error"), repo, err)
			continue
		}
		removed++
	}
	fmt.Printf("%s  %s\n", paint(bold, "HOOKS"), plural(removed, "hook")+" removed")
	return nil
}

func stateColor(s hooks.State) string {
	switch s {
	case hooks.StateMissing:
		return red
	case hooks.StateForeign:
		return yellow
	case hooks.StateOutdated:
		return blue
	case hooks.StateError:
		return red
	}
	return green
}

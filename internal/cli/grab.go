package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/madelinecameron/poppy/internal/grab"
)

func runGrab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grab", flag.ContinueOnError)
	force := fs.Bool("force", false, "apply even over local changes or a diverged branch")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("grab: name what to teleport, e.g. `poppy grab devbox:poppy [branch]`")
	}

	sourceMachine, repoQuery, err := parseGrabTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	branch := ""
	if fs.NArg() > 1 {
		branch = fs.Arg(1)
	}

	cfg, store, err := setup(ctx)
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

	target, err := grab.Resolve(fleet, cfg.Machine, sourceMachine, repoQuery, branch)
	if err != nil {
		return err
	}

	fmt.Printf("%s %s from %s onto %s\n",
		paint(dim, "teleporting"),
		paint(bold, target.RepoName+":"+target.Branch),
		paint(cyan, target.SourceMachine),
		paint(dim, target.LocalPath))
	fmt.Printf("%s %s taken %s\n",
		paint(dim, "  snapshot"),
		paint(bold, shortSHA(target.Snapshot.SHA)),
		paint(dim, relTime(target.Snapshot.Taken, timeNow())))

	if err := grab.Execute(ctx, cfg, target, *force); err != nil {
		return err
	}

	fmt.Println(paint(green, "grabbed"))
	fmt.Println(paint(dim, "  the work is now on this machine; review it with `git status`"))
	fmt.Println(paint(dim, fmt.Sprintf("  it still exists on %s — commit here or there, not both", target.SourceMachine)))
	return nil
}

// parseGrabTarget splits "machine:repo" into its parts.
func parseGrabTarget(arg string) (machine, repo string, err error) {
	machine, repo, ok := strings.Cut(arg, ":")
	if !ok || machine == "" || repo == "" {
		return "", "", fmt.Errorf("target %q must be machine:repo, e.g. devbox:poppy", arg)
	}
	return machine, repo, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

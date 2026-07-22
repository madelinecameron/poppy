package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

// repoArg resolves a command's repository argument, defaulting to the
// repository containing the working directory.
func repoArg(args []string) (string, error) {
	target := "."
	if len(args) > 0 {
		target = args[0]
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return "", fmt.Errorf("%s is not a git repository", abs)
	}
	return abs, nil
}

func runSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	repo, err := repoArg(fs.Args())
	if err != nil {
		return err
	}

	s := snapshot.New(cfg.Machine)
	s.MaxFileBytes = cfg.Snapshot.MaxFileBytes

	snap, err := s.Create(ctx, repo)
	switch {
	case errors.Is(err, snapshot.ErrNothingToCapture):
		fmt.Println(paint(dim, "working tree is clean; nothing to capture"))
		return nil
	case errors.Is(err, snapshot.ErrUnchanged):
		fmt.Println(paint(dim, "unchanged since the last snapshot"))
		return nil
	case err != nil:
		return err
	}

	fmt.Printf("%s %s on %s  %s\n",
		paint(green, "captured"),
		paint(bold, snap.SHA[:12]),
		paint(cyan, snap.Branch),
		paint(dim, plural(snap.Files, "change")))

	if len(snap.Skipped) > 0 {
		fmt.Println(paint(yellow, fmt.Sprintf("  omitted %s over the size limit:", plural(len(snap.Skipped), "file"))))
		for _, f := range snap.Skipped {
			fmt.Println(paint(dim, "    "+f))
		}
	}
	return nil
}

func runTimeline(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	branch := fs.String("branch", "", "only show snapshots of this branch")
	machine := fs.String("machine", "", "only show snapshots from this machine")
	limit := fs.Int("limit", 30, "maximum snapshots to list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, err := repoArg(fs.Args())
	if err != nil {
		return err
	}

	snaps, err := snapshot.List(ctx, repo, *machine, *branch)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Println("No snapshots recorded for this repository.")
		fmt.Println(paint(dim, "run `poppy snapshot` to capture the working tree now"))
		return nil
	}

	now := time.Now()
	var t table
	t.setHeader("WHEN", "AGE", "MACHINE", "BRANCH", "SNAPSHOT")
	shown := snaps
	if len(shown) > *limit {
		shown = shown[:*limit]
	}
	for _, s := range shown {
		t.add(
			s.Taken.Local().Format("2006-01-02 15:04"),
			paint(dim, snapshot.FormatAge(now.Sub(s.Taken))),
			paint(cyan, s.Machine),
			s.Branch,
			paint(dim, s.SHA[:12]),
		)
	}
	t.render(os.Stdout, "")

	if hidden := len(snaps) - len(shown); hidden > 0 {
		fmt.Println(paint(dim, fmt.Sprintf("… %d older (use --limit)", hidden)))
	}
	return nil
}

func runRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	asOf := fs.String("as-of", "", "restore the snapshot in effect at this time (default: the newest)")
	sha := fs.String("snapshot", "", "restore this specific snapshot")
	branch := fs.String("branch", "", "restrict to snapshots of this branch")
	machine := fs.String("machine", "", "restrict to snapshots from this machine")
	force := fs.Bool("force", false, "apply even though the working tree has changes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, err := repoArg(fs.Args())
	if err != nil {
		return err
	}

	target := *sha
	if target == "" {
		snaps, err := snapshot.List(ctx, repo, *machine, *branch)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			return errors.New("no snapshots recorded for this repository")
		}

		// With no time given, restore the newest snapshot. Recovering the work
		// you just lost is the common case and should need no arguments.
		found := snaps[0]
		if *asOf != "" {
			when, err := parseWhen(*asOf, time.Now())
			if err != nil {
				return err
			}
			resolved, ok := snapshot.Resolve(snaps, when)
			if !ok {
				oldest := snaps[len(snaps)-1]
				return fmt.Errorf("no snapshot at or before %s; the oldest is from %s (see `poppy timeline`)",
					when.Local().Format(time.DateTime),
					oldest.Taken.Local().Format(time.DateTime))
			}
			found = resolved
		}

		target = found.SHA
		fmt.Printf("%s %s from %s on %s\n",
			paint(dim, "restoring"),
			paint(bold, found.SHA[:12]),
			found.Taken.Local().Format(time.DateTime),
			paint(cyan, found.Branch))
	}

	if err := snapshot.Restore(ctx, repo, target, *force); err != nil {
		return err
	}
	fmt.Println(paint(green, "restored"))
	return nil
}

// parseWhen accepts absolute timestamps and relative durations such as "2h"
// or "3d", which is how people describe when work was last intact.
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}

	if days, ok := strings.CutSuffix(s, "d"); ok {
		if d, err := time.ParseDuration(days + "h"); err == nil {
			return now.Add(-d * 24), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}

	for _, layout := range []string{time.RFC3339, time.DateTime, "2006-01-02 15:04", time.DateOnly} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time; try 2h, 3d, or 2026-07-20 14:30", s)
}

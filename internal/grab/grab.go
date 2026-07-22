// Package grab teleports uncommitted work from another machine onto this one.
//
// The source machine's agent has already captured the work as a snapshot and
// pushed it to the project's mirror. Grab fetches that snapshot and applies it,
// reproducing the source's exact working state — its committed HEAD plus the
// staged, unstaged, and untracked changes on top. Nothing is merged silently:
// a destination that has diverged from the snapshot's base is refused with an
// explanation rather than resolved by guesswork.
package grab

import (
	"context"
	"fmt"
	"strings"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/mirror"
	"github.com/madelinecameron/poppy/internal/model"
)

// Target describes a resolved teleport: which snapshot to fetch, from where,
// and which local working tree receives it.
type Target struct {
	SourceMachine string
	RepoID        string
	RepoName      string
	Branch        string
	Snapshot      model.SnapshotRef
	LocalPath     string
}

// Resolve works out what "grab <machine>:<repo> [branch]" refers to, reading
// the fleet state to find the source snapshot and the matching local checkout.
func Resolve(fleet model.Fleet, thisMachine, sourceMachine, repoQuery, branch string) (Target, error) {
	var src *model.MachineState
	for i := range fleet.Machines {
		if fleet.Machines[i].Manifest.Machine == sourceMachine {
			src = &fleet.Machines[i]
			break
		}
	}
	if src == nil {
		return Target{}, fmt.Errorf("no machine named %q in the fleet", sourceMachine)
	}

	sourceRepo, err := matchRepo(src.Repos, repoQuery, branch)
	if err != nil {
		return Target{}, err
	}
	if sourceRepo.Snapshot == nil {
		return Target{}, fmt.Errorf("%s:%s has no snapshot to grab (is it dirty and synced?)",
			sourceMachine, sourceRepo.Name)
	}
	if !sourceRepo.Snapshot.Synced {
		return Target{}, fmt.Errorf("%s:%s has a snapshot but it has not reached the mirror yet",
			sourceMachine, sourceRepo.Name)
	}

	wantBranch := branch
	if wantBranch == "" {
		wantBranch = sourceRepo.Snapshot.Branch
	}

	local, err := localCheckout(fleet, thisMachine, sourceRepo.RepoID)
	if err != nil {
		return Target{}, err
	}

	return Target{
		SourceMachine: sourceMachine,
		RepoID:        sourceRepo.RepoID,
		RepoName:      sourceRepo.Name,
		Branch:        wantBranch,
		Snapshot:      *sourceRepo.Snapshot,
		LocalPath:     local,
	}, nil
}

// matchRepo finds the source repository named by a query, preferring an exact
// name, then a unique substring, then a repoId. When a branch is given, only
// repositories with a snapshot of that branch are considered.
func matchRepo(repos []model.Repo, query, branch string) (model.Repo, error) {
	q := strings.ToLower(query)
	var exact, partial []model.Repo

	for _, r := range repos {
		if branch != "" && (r.Snapshot == nil || r.Snapshot.Branch != branch) {
			continue
		}
		name := strings.ToLower(r.Name)
		switch {
		case name == q || r.RepoID == query:
			exact = append(exact, r)
		case strings.Contains(name, q):
			partial = append(partial, r)
		}
	}

	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return model.Repo{}, ambiguous(query, exact)
	case len(partial) == 1:
		return partial[0], nil
	case len(partial) > 1:
		return model.Repo{}, ambiguous(query, partial)
	}
	return model.Repo{}, fmt.Errorf("no repository matching %q on that machine", query)
}

func ambiguous(query string, repos []model.Repo) error {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return fmt.Errorf("%q matches several repositories: %s", query, strings.Join(names, ", "))
}

// localCheckout finds where this machine has the project checked out. Grab
// applies work into an existing checkout rather than cloning, so the project
// must already be present.
func localCheckout(fleet model.Fleet, thisMachine, repoID string) (string, error) {
	for _, m := range fleet.Machines {
		if m.Manifest.Machine != thisMachine {
			continue
		}
		for _, r := range m.Repos {
			if r.RepoID == repoID && !r.Broken {
				return r.Path, nil
			}
		}
	}
	return "", fmt.Errorf("this machine has no checkout of that project; clone it first, then grab")
}

// Execute fetches the target snapshot from the mirror and applies it.
func Execute(ctx context.Context, cfg config.Config, t Target, force bool) error {
	mirrorCfg := cfg.Snapshot.Mirror.MirrorConfig()
	if !mirrorCfg.Enabled() {
		return fmt.Errorf("no mirror configured; grab needs snapshot.mirror set")
	}

	fetched, ok, err := mirror.For(mirrorCfg, t.RepoID).FetchInto(ctx, t.LocalPath, t.SourceMachine)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no snapshot arrived from %s for this project", t.SourceMachine)
	}

	sha := t.Snapshot.SHA
	if sha == "" {
		sha = fetched.SHA
	}
	return Apply(ctx, t.LocalPath, sha, t.Branch, force)
}

// Apply restores a fetched snapshot onto the working tree at repoPath.
//
// The snapshot's first parent is the source machine's HEAD at capture, so the
// branch is placed on that commit and the snapshot applied over it, giving the
// source's exact state. A local branch that already sits on a different commit
// is a divergence: applying over it would silently blend two lines of work, so
// it is refused unless forced.
func Apply(ctx context.Context, repoPath, sha, branch string, force bool) error {
	g := gitx.New(repoPath)

	if err := verifyPresent(ctx, g, sha); err != nil {
		return err
	}

	if !force {
		status, err := g.Status(ctx)
		if err != nil {
			return err
		}
		if status.Dirty.Any() {
			return fmt.Errorf("this working tree has %d uncommitted change(s); commit or stash them first, or pass --force",
				status.Dirty.Total())
		}
	}

	base, _ := g.Run(ctx, "rev-parse", "--verify", "--quiet", sha+"^1")

	branchHead, _ := g.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	switch {
	case branchHead == "":
		if err := createBranch(ctx, g, branch, base); err != nil {
			return err
		}
	case base != "" && branchHead != base && !force:
		return fmt.Errorf("local branch %q has diverged from the snapshot's base\n"+
			"  local %s is at %s\n"+
			"  the snapshot was taken on %s\n"+
			"  reconcile the branches, or pass --force to apply over your local commits",
			branch, branch, short(branchHead), short(base))
	default:
		if _, err := g.Run(ctx, "checkout", branch); err != nil {
			return fmt.Errorf("switch to %s: %w", branch, err)
		}
	}

	if _, err := g.Run(ctx, "stash", "apply", sha); err != nil {
		return fmt.Errorf("apply snapshot: %w", err)
	}
	return nil
}

func createBranch(ctx context.Context, g *gitx.Runner, branch, base string) error {
	args := []string{"checkout", "-b", branch}
	if base != "" {
		args = append(args, base)
	}
	if _, err := g.Run(ctx, args...); err != nil {
		return fmt.Errorf("create branch %s: %w", branch, err)
	}
	return nil
}

func verifyPresent(ctx context.Context, g *gitx.Runner, sha string) error {
	if _, err := g.Run(ctx, "cat-file", "-e", sha); err != nil {
		return fmt.Errorf("snapshot %s is not present locally after fetch", short(sha))
	}
	return nil
}

// Divergences reports projects that are dirty on more than one machine on the
// same branch, which is work heading toward a conflict before it happens.
func Divergences(fleet model.Fleet) []Divergence {
	type key struct{ repoID, branch string }
	seen := make(map[key][]dirtyOn)

	for _, m := range fleet.Machines {
		for _, r := range m.Repos {
			if !r.Dirty.Any() || r.Head.Branch == "" {
				continue
			}
			k := key{r.RepoID, r.Head.Branch}
			seen[k] = append(seen[k], dirtyOn{machine: m.Manifest.Machine, repo: r})
		}
	}

	var out []Divergence
	for k, places := range seen {
		if len(places) < 2 {
			continue
		}
		d := Divergence{RepoID: k.repoID, Branch: k.branch, Name: places[0].repo.Name}
		for _, p := range places {
			d.Machines = append(d.Machines, p.machine)
		}
		out = append(out, d)
	}
	return out
}

type dirtyOn struct {
	machine string
	repo    model.Repo
}

// Divergence is a project dirty on the same branch on multiple machines.
type Divergence struct {
	RepoID   string
	Name     string
	Branch   string
	Machines []string
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

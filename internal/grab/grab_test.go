package grab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/mirror"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// twoMachines sets up a shared mirror, a source repo with captured+synced work,
// and a destination checkout of the same project, returning what grab needs.
func twoMachines(t *testing.T) (cfg config.Config, fleet model.Fleet, source, dest *gittest.Repo) {
	t.Helper()
	ctx := context.Background()
	mirrorDir := t.TempDir()
	cfg = config.Config{
		Machine: "laptop",
		Snapshot: config.Snapshot{
			Enabled: true,
			Mirror:  config.Mirror{Enabled: true, Dir: mirrorDir},
		},
	}

	// devbox: a project with a shared base commit.
	source = gittest.New(t, "main")
	source.Commit("shared history", "app.go", "package main\n")

	// laptop: a genuine clone of the same project, so it holds the identical
	// base commit — as any two checkouts of one repository would.
	destDir := filepath.Join(t.TempDir(), "laptop-checkout")
	if out, err := runGit(t.TempDir(), "clone", "--quiet", source.Path, destDir); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	dest = &gittest.Repo{T: t, Path: destDir}
	dest.Git("config", "user.name", "test")
	dest.Git("config", "user.email", "test@example.com")

	// devbox now does real work in progress, captured and pushed to the mirror.
	source.Write("app.go", "package main // work in progress\n")
	source.Write("notes.md", "design notes\n")
	source.Git("add", "notes.md")

	snap, err := snapshot.New("devbox").Create(ctx, source.Path)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	repoID := "proj-shared"
	if err := mirror.For(mirror.Config{Dir: mirrorDir}, repoID).Push(ctx, source.Path, "devbox"); err != nil {
		t.Fatalf("push: %v", err)
	}

	fleet = model.Fleet{Machines: []model.MachineState{
		{
			Manifest: model.Manifest{Machine: "devbox"},
			Repos: []model.Repo{{
				RepoID: repoID, Name: "proj", Machine: "devbox", Path: source.Path,
				Head:  model.Head{Branch: "main"},
				Dirty: model.Dirty{Unstaged: 1, Staged: 1},
				Snapshot: &model.SnapshotRef{
					SHA: snap.SHA, Ref: snap.Ref, Branch: "main", Taken: snap.Taken, Synced: true,
				},
			}},
		},
		{
			Manifest: model.Manifest{Machine: "laptop"},
			Repos: []model.Repo{{
				RepoID: repoID, Name: "proj", Machine: "laptop", Path: dest.Path,
				Head: model.Head{Branch: "main"},
			}},
		},
	}}
	return cfg, fleet, source, dest
}

// The headline promise: work in progress on devbox lands on the laptop.
func TestGrabTeleportsWorkBetweenMachines(t *testing.T) {
	ctx := context.Background()
	cfg, fleet, _, dest := twoMachines(t)

	target, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Branch != "main" || target.LocalPath != dest.Path {
		t.Fatalf("target = %+v", target)
	}

	if err := Execute(ctx, cfg, target, false); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if got := readFile(t, dest.Path, "app.go"); got != "package main // work in progress\n" {
		t.Errorf("app.go = %q, want the devbox edit", got)
	}
	if got := readFile(t, dest.Path, "notes.md"); got != "design notes\n" {
		t.Errorf("notes.md = %q, want the staged untracked file", got)
	}

	// The staged/unstaged split should survive the teleport.
	staged := dest.Git("diff", "--cached", "--name-only")
	if !strings.Contains(staged, "notes.md") {
		t.Errorf("notes.md not staged after grab; staged = %q", staged)
	}
	unstaged := dest.Git("diff", "--name-only")
	if !strings.Contains(unstaged, "app.go") {
		t.Errorf("app.go not an unstaged change after grab; unstaged = %q", unstaged)
	}
	if strings.Contains(staged, "app.go") {
		t.Errorf("app.go was staged; the split was not preserved. staged = %q", staged)
	}
}

func TestGrabRefusesWhenDestinationIsDirty(t *testing.T) {
	ctx := context.Background()
	cfg, fleet, _, dest := twoMachines(t)
	dest.Write("app.go", "my own uncommitted change\n")

	target, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err != nil {
		t.Fatal(err)
	}

	err = Execute(ctx, cfg, target, false)
	if err == nil {
		t.Fatal("grab onto a dirty tree was allowed")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error = %v, want it to explain the refusal", err)
	}
	if got := readFile(t, dest.Path, "app.go"); got != "my own uncommitted change\n" {
		t.Error("refused grab still modified the working tree")
	}
}

// If the destination branch has its own commits the source never saw, applying
// over them would blend two lines of work. Grab must refuse and say why.
func TestGrabRefusesDivergedBranch(t *testing.T) {
	ctx := context.Background()
	cfg, fleet, _, dest := twoMachines(t)

	dest.Commit("laptop's own commit", "local.go", "package main\n")

	target, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err != nil {
		t.Fatal(err)
	}

	err = Execute(ctx, cfg, target, false)
	if err == nil {
		t.Fatal("grab onto a diverged branch was allowed")
	}
	if !strings.Contains(err.Error(), "diverged") {
		t.Errorf("error = %v, want a divergence explanation", err)
	}
}

func TestGrabForceAppliesOverDivergedBranch(t *testing.T) {
	ctx := context.Background()
	cfg, fleet, _, dest := twoMachines(t)
	dest.Commit("laptop's own commit", "local.go", "package main\n")

	target, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, cfg, target, true); err != nil {
		t.Fatalf("forced grab: %v", err)
	}
	if got := readFile(t, dest.Path, "app.go"); got != "package main // work in progress\n" {
		t.Errorf("app.go = %q, want the devbox edit applied under force", got)
	}
}

// Grab onto a machine that has never checked out the project should create the
// branch from the snapshot's base, since the base object arrives with the fetch.
func TestGrabCreatesMissingBranch(t *testing.T) {
	ctx := context.Background()
	cfg, fleet, _, dest := twoMachines(t)

	// Rename the destination's branch so the target branch is absent.
	dest.Git("branch", "-m", "main", "other")

	target, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, cfg, target, false); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if branch := dest.Git("rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Errorf("HEAD on %q, want the created main branch", branch)
	}
	if got := readFile(t, dest.Path, "app.go"); got != "package main // work in progress\n" {
		t.Errorf("app.go = %q", got)
	}
}

func TestResolveRejectsUnsyncedSnapshot(t *testing.T) {
	_, fleet, _, _ := twoMachines(t)
	fleet.Machines[0].Repos[0].Snapshot.Synced = false

	_, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err == nil || !strings.Contains(err.Error(), "not reached the mirror") {
		t.Errorf("err = %v, want a not-yet-synced explanation", err)
	}
}

func TestResolveRejectsUnknownMachine(t *testing.T) {
	_, fleet, _, _ := twoMachines(t)
	if _, err := Resolve(fleet, "laptop", "nas", "proj", ""); err == nil {
		t.Error("resolved a machine not in the fleet")
	}
}

func TestResolveRequiresLocalCheckout(t *testing.T) {
	_, fleet, _, _ := twoMachines(t)
	// Drop laptop's checkout of the project.
	fleet.Machines[1].Repos = nil

	_, err := Resolve(fleet, "laptop", "devbox", "proj", "")
	if err == nil || !strings.Contains(err.Error(), "no checkout") {
		t.Errorf("err = %v, want a missing-checkout explanation", err)
	}
}

func TestDivergencesFindsSameBranchDirtyOnMultipleMachines(t *testing.T) {
	fleet := model.Fleet{Machines: []model.MachineState{
		{
			Manifest: model.Manifest{Machine: "devbox"},
			Repos: []model.Repo{
				{RepoID: "p1", Name: "poppy", Head: model.Head{Branch: "main"}, Dirty: model.Dirty{Unstaged: 1}},
				{RepoID: "p2", Name: "api", Head: model.Head{Branch: "main"}, Dirty: model.Dirty{Unstaged: 1}},
			},
		},
		{
			Manifest: model.Manifest{Machine: "laptop"},
			Repos: []model.Repo{
				{RepoID: "p1", Name: "poppy", Head: model.Head{Branch: "main"}, Dirty: model.Dirty{Unstaged: 2}},
				{RepoID: "p2", Name: "api", Head: model.Head{Branch: "main"}}, // clean here
			},
		},
	}}

	got := Divergences(fleet)
	if len(got) != 1 {
		t.Fatalf("divergences = %d, want 1 (only poppy dirty on both)", len(got))
	}
	if got[0].Name != "poppy" || got[0].Branch != "main" {
		t.Errorf("divergence = %+v", got[0])
	}
	if len(got[0].Machines) != 2 {
		t.Errorf("machines = %v, want two", got[0].Machines)
	}
}

func TestDivergencesIgnoresDifferentBranches(t *testing.T) {
	fleet := model.Fleet{Machines: []model.MachineState{
		{Manifest: model.Manifest{Machine: "devbox"}, Repos: []model.Repo{
			{RepoID: "p1", Name: "poppy", Head: model.Head{Branch: "feature"}, Dirty: model.Dirty{Unstaged: 1}},
		}},
		{Manifest: model.Manifest{Machine: "laptop"}, Repos: []model.Repo{
			{RepoID: "p1", Name: "poppy", Head: model.Head{Branch: "main"}, Dirty: model.Dirty{Unstaged: 1}},
		}},
	}}
	if got := Divergences(fleet); len(got) != 0 {
		t.Errorf("divergences = %+v, want none (different branches)", got)
	}
}

func TestParseTargetBranchFiltersSnapshots(t *testing.T) {
	// A repo whose snapshot is of a different branch must not match a branch
	// request, since grab would then apply the wrong work.
	_, fleet, _, _ := twoMachines(t)
	_, err := Resolve(fleet, "laptop", "devbox", "proj", "nonexistent-branch")
	if err == nil {
		t.Error("resolved a branch with no matching snapshot")
	}
	_ = time.Now
}

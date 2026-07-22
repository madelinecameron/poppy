package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/gitx"
)

func dirtyRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.New(t, "main")
	r.Commit("init", "tracked.txt", "v1\n")
	r.Write("tracked.txt", "v2\n")
	r.Write("staged.txt", "staged\n")
	r.Git("add", "staged.txt")
	r.Write("untracked.txt", "untracked\n")
	return r
}

func TestCreateCapturesWorkingTree(t *testing.T) {
	repo := dirtyRepo(t)

	snap, err := New("laptop").Create(context.Background(), repo.Path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if snap.SHA == "" || snap.Branch != "main" || snap.Machine != "laptop" {
		t.Errorf("snapshot = %+v", snap)
	}
	if snap.Files != 3 {
		t.Errorf("files = %d, want 3", snap.Files)
	}

	if got := repo.Git("rev-parse", LatestRef("laptop", "main")); got != snap.SHA {
		t.Errorf("latest ref = %s, want %s", got, snap.SHA)
	}
	if got := repo.Git("rev-parse", snap.Ref); got != snap.SHA {
		t.Errorf("history ref = %s, want %s", got, snap.SHA)
	}
}

// A capture must be invisible to the person working in the repository.
func TestCreateLeavesWorkingTreeAndIndexUntouched(t *testing.T) {
	repo := dirtyRepo(t)

	before := repo.Git("status", "--porcelain")
	beforeHead := repo.Git("rev-parse", "HEAD")

	if _, err := New("laptop").Create(context.Background(), repo.Path); err != nil {
		t.Fatalf("create: %v", err)
	}

	if after := repo.Git("status", "--porcelain"); after != before {
		t.Errorf("status changed:\nbefore %q\nafter  %q", before, after)
	}
	if after := repo.Git("rev-parse", "HEAD"); after != beforeHead {
		t.Errorf("HEAD moved: %s -> %s", beforeHead, after)
	}
	if branch := repo.Git("rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

// The point of the stash shape: a restore rebuilds the three-way split.
func TestSnapshotRoundTripPreservesStagedUnstagedUntrackedSplit(t *testing.T) {
	ctx := context.Background()
	repo := dirtyRepo(t)

	snap, err := New("laptop").Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wanted := repo.Git("status", "--porcelain")

	repo.Git("checkout", "-q", "--", "tracked.txt")
	repo.Git("reset", "-q")
	if err := os.Remove(filepath.Join(repo.Path, "staged.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo.Path, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if got := repo.Git("status", "--porcelain"); got != "" {
		t.Fatalf("setup left the tree dirty: %q", got)
	}

	if err := Restore(ctx, repo.Path, snap.SHA, false); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if got := repo.Git("status", "--porcelain"); got != wanted {
		t.Errorf("restored status:\n got %q\nwant %q", got, wanted)
	}
	if content := readFile(t, repo.Path, "tracked.txt"); content != "v2\n" {
		t.Errorf("tracked.txt = %q, want v2", content)
	}
	if content := readFile(t, repo.Path, "untracked.txt"); content != "untracked\n" {
		t.Errorf("untracked.txt = %q", content)
	}
}

// Snapshots are pushed to a shared hub, so anything a gitignore excludes must
// never enter the object store. This is the privacy boundary.
func TestCreateNeverCapturesIgnoredFiles(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Write(".gitignore", "secrets.env\n*.key\nbuild/\n")
	repo.Commit("init", "a.txt", "one\n")

	repo.Write("secrets.env", "API_TOKEN=hunter2\n")
	repo.Write("server.key", "PRIVATE KEY MATERIAL\n")
	repo.Write("build/artifact.bin", "compiled output\n")
	repo.Write("real-work.txt", "keep me\n")

	snap, err := New("laptop").Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	listed := repo.Git("ls-tree", "-r", "--name-only", snap.SHA+"^3")
	for _, forbidden := range []string{"secrets.env", "server.key", "build/artifact.bin"} {
		if strings.Contains(listed, forbidden) {
			t.Errorf("ignored file %q captured in snapshot", forbidden)
		}
	}
	if !strings.Contains(listed, "real-work.txt") {
		t.Errorf("untracked work missing from snapshot; captured: %q", listed)
	}
}

// Untracked build output can be enormous, and copying it into the object store
// would bloat the repository permanently.
func TestCreateSkipsOversizedUntrackedFiles(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("small.txt", "keep\n")
	repo.Write("huge.bin", strings.Repeat("x", 4096))

	s := New("laptop")
	s.MaxFileBytes = 1024

	snap, err := s.Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(snap.Skipped) != 1 || snap.Skipped[0] != "huge.bin" {
		t.Errorf("skipped = %v, want [huge.bin]", snap.Skipped)
	}

	listed := repo.Git("ls-tree", "-r", "--name-only", snap.SHA+"^3")
	if strings.Contains(listed, "huge.bin") {
		t.Error("oversized file captured despite the limit")
	}
	if !strings.Contains(listed, "small.txt") {
		t.Errorf("small file dropped; captured: %q", listed)
	}
}

func TestCreateRefusesCleanTree(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	_, err := New("laptop").Create(context.Background(), repo.Path)
	if !errors.Is(err, ErrNothingToCapture) {
		t.Errorf("err = %v, want ErrNothingToCapture", err)
	}
}

// Scans run on a timer, so an unchanged tree must not accumulate identical
// snapshots on every pass.
func TestCreateSkipsUnchangedTree(t *testing.T) {
	ctx := context.Background()
	repo := dirtyRepo(t)
	s := New("laptop")

	if _, err := s.Create(ctx, repo.Path); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.Create(ctx, repo.Path); !errors.Is(err, ErrUnchanged) {
		t.Errorf("second create err = %v, want ErrUnchanged", err)
	}

	repo.Write("tracked.txt", "v3\n")
	if _, err := s.Create(ctx, repo.Path); err != nil {
		t.Errorf("create after real change: %v", err)
	}

	snaps, err := List(ctx, repo.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Errorf("snapshots = %d, want 2", len(snaps))
	}
}

// Captures taken in quick succession must each be recorded. A ref name with
// only second precision would have the later capture silently replace the
// earlier one, losing an intermediate state the time machine promised to keep.
func TestCreateRecordsRapidSuccessiveCaptures(t *testing.T) {
	ctx := context.Background()
	repo := dirtyRepo(t)
	s := New("laptop")

	frozen := time.Now()
	s.Now = func() time.Time { return frozen }

	for i := range 3 {
		repo.Write("tracked.txt", "revision "+string(rune('a'+i))+"\n")
		if _, err := s.Create(ctx, repo.Path); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	snaps, err := List(ctx, repo.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("snapshots = %d, want 3 distinct captures at one instant", len(snaps))
	}
	seen := map[string]bool{}
	for _, snap := range snaps {
		if seen[snap.Ref] {
			t.Errorf("duplicate ref %s", snap.Ref)
		}
		seen[snap.Ref] = true
	}
}

func TestCreateHandlesUnbornBranch(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Write("first-work.txt", "before any commit\n")

	snap, err := New("laptop").Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("create on unborn branch: %v", err)
	}
	if snap.Branch != "main" {
		t.Errorf("branch = %q, want main", snap.Branch)
	}
	listed := repo.Git("ls-tree", "-r", "--name-only", snap.SHA+"^2")
	if strings.Contains(listed, "first-work.txt") {
		return // captured through the index
	}
	if listed := repo.Git("ls-tree", "-r", "--name-only", snap.SHA+"^3"); !strings.Contains(listed, "first-work.txt") {
		t.Errorf("work before the first commit was not captured: %q", listed)
	}
}

// Slashes in branch names would otherwise collide with the ref hierarchy.
func TestCreateHandlesSlashedBranchNames(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Git("checkout", "-q", "-b", "feature/auth/refactor")
	repo.Write("wip.txt", "work\n")

	snap, err := New("laptop").Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if snap.Branch != "feature/auth/refactor" {
		t.Errorf("branch = %q", snap.Branch)
	}

	snaps, err := List(ctx, repo.Path, "laptop", "feature/auth/refactor")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Branch != "feature/auth/refactor" {
		t.Errorf("list = %+v, want the slashed branch decoded", snaps)
	}
}

// Git forbids branches "feature" and "feature/auth" at once, but nothing stops
// the first from being deleted and the second created later. The snapshot ref
// for the old branch outlives it, so an unescaped branch name would leave
// refs/poppy/wip/<machine>/feature blocking .../feature/auth: git cannot store
// a ref at a path that is already a file.
func TestBranchEncodingSurvivesBranchReplacedByNestedName(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	repo.Git("checkout", "-q", "-b", "feature")
	repo.Write("wip.txt", "work on feature\n")
	if _, err := New("laptop").Create(ctx, repo.Path); err != nil {
		t.Fatalf("create on feature: %v", err)
	}

	repo.Git("checkout", "-q", "main")
	repo.Git("branch", "-qD", "feature")
	repo.Git("checkout", "-q", "-b", "feature/auth")
	repo.Write("wip.txt", "work on feature/auth\n")
	if _, err := New("laptop").Create(ctx, repo.Path); err != nil {
		t.Fatalf("create on feature/auth after feature was deleted: %v", err)
	}

	snaps, err := List(ctx, repo.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range snaps {
		seen[s.Branch] = true
	}
	if !seen["feature"] || !seen["feature/auth"] {
		t.Errorf("branches = %v, want both feature and feature/auth retained", seen)
	}
}

func TestRestoreRefusesDirtyTreeWithoutForce(t *testing.T) {
	ctx := context.Background()
	repo := dirtyRepo(t)

	snap, err := New("laptop").Create(ctx, repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	repo.Write("other-work.txt", "different work\n")

	err = Restore(ctx, repo.Path, snap.SHA, false)
	if err == nil {
		t.Fatal("restore over a dirty tree was allowed")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error = %v, want it to explain the refusal", err)
	}
	if content := readFile(t, repo.Path, "other-work.txt"); content != "different work\n" {
		t.Error("refused restore still modified the working tree")
	}
}

// Snapshots must be transferable: that is what makes them a backup and what
// poppy grab will later rely on.
func TestSnapshotsTransferBetweenClones(t *testing.T) {
	ctx := context.Background()
	source := dirtyRepo(t)

	snap, err := New("devbox").Create(ctx, source.Path)
	if err != nil {
		t.Fatal(err)
	}

	destDir := filepath.Join(t.TempDir(), "clone")
	if _, err := gitx.New(t.TempDir()).Run(ctx, "clone", "--quiet", source.Path, destDir); err != nil {
		t.Fatalf("clone: %v", err)
	}
	dest := &gittest.Repo{T: t, Path: destDir}
	dest.Git("config", "user.name", "test")
	dest.Git("config", "user.email", "test@example.com")
	dest.Git("fetch", "--quiet", source.Path, "refs/poppy/*:refs/poppy/*")

	if err := Restore(ctx, destDir, snap.SHA, false); err != nil {
		t.Fatalf("restore in clone: %v", err)
	}
	if content := readFile(t, destDir, "tracked.txt"); content != "v2\n" {
		t.Errorf("tracked.txt = %q, want the captured v2", content)
	}
	if content := readFile(t, destDir, "untracked.txt"); content != "untracked\n" {
		t.Errorf("untracked.txt = %q, want it restored in the clone", content)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestResolveFindsSnapshotInEffectAtATime(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{SHA: "c", Taken: base.Add(2 * time.Hour)},
		{SHA: "b", Taken: base.Add(time.Hour)},
		{SHA: "a", Taken: base},
	}

	got, ok := Resolve(snaps, base.Add(90*time.Minute))
	if !ok || got.SHA != "b" {
		t.Errorf("resolve mid-range = %+v, want b", got)
	}
	if got, ok := Resolve(snaps, base.Add(3*time.Hour)); !ok || got.SHA != "c" {
		t.Errorf("resolve after last = %+v, want c", got)
	}
	if _, ok := Resolve(snaps, base.Add(-time.Hour)); ok {
		t.Error("resolved a time before every snapshot")
	}
}

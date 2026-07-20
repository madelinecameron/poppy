package mirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mads/poppy/internal/gittest"
	"github.com/mads/poppy/internal/snapshot"
)

func dirtyRepoWithSnapshot(t *testing.T, machine string) (*gittest.Repo, snapshot.Snapshot) {
	t.Helper()
	repo := gittest.New(t, "main")
	repo.Commit("init", "tracked.txt", "v1\n")
	repo.Write("tracked.txt", "wip\n")
	repo.Write("untracked.txt", "new work\n")

	snap, err := snapshot.New(machine).Create(context.Background(), repo.Path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	return repo, *snap
}

func TestPushCreatesMirrorAndTransfersSnapshot(t *testing.T) {
	ctx := context.Background()
	repo, snap := dirtyRepoWithSnapshot(t, "devbox")

	cfg := LocalConfigForTest(t.TempDir())
	m := For(cfg, "repo-abc")

	if err := m.Push(ctx, repo.Path, "devbox"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// The bare mirror should now hold the snapshot and all its objects.
	bare := filepath.Join(cfg.Dir, "repo-abc.git")
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("mirror not created: %v", err)
	}
	got := gittest.Repo{T: t, Path: bare}
	if sha := got.Git("rev-parse", snap.Ref); sha != snap.SHA {
		t.Errorf("mirror ref = %s, want %s", sha, snap.SHA)
	}
	// The working tree object must have travelled with the ref.
	if typ := got.Git("cat-file", "-t", snap.SHA+"^{tree}"); typ != "tree" {
		t.Errorf("snapshot tree missing from mirror: %q", typ)
	}
}

// The whole point of a mirror: a machine that has never seen the project can
// still recover another machine's work from it.
func TestFetchIntoRecoversSnapshotOnAFreshCheckout(t *testing.T) {
	ctx := context.Background()
	source, snap := dirtyRepoWithSnapshot(t, "devbox")

	cfg := LocalConfigForTest(t.TempDir())
	if err := For(cfg, "proj").Push(ctx, source.Path, "devbox"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// A separate repository standing in for another machine's checkout, which
	// shares none of devbox's snapshot objects until the fetch.
	dest := gittest.New(t, "main")
	dest.Commit("init", "tracked.txt", "v1\n")

	fetched, ok, err := For(cfg, "proj").FetchInto(ctx, dest.Path, "devbox")
	if err != nil {
		t.Fatalf("fetch into: %v", err)
	}
	if !ok {
		t.Fatal("no snapshot fetched")
	}
	if fetched.SHA != snap.SHA {
		t.Errorf("fetched %s, want %s", fetched.SHA, snap.SHA)
	}
	if typ := dest.Git("cat-file", "-t", snap.SHA); typ != "commit" {
		t.Errorf("snapshot commit not present in destination: %q", typ)
	}
}

// A machine fetching another's work must not have its own snapshots disturbed.
func TestFetchMachineLeavesOwnSnapshotsAlone(t *testing.T) {
	ctx := context.Background()
	cfg := LocalConfigForTest(t.TempDir())

	// devbox captures and pushes.
	devbox, _ := dirtyRepoWithSnapshot(t, "devbox")
	if err := For(cfg, "proj").Push(ctx, devbox.Path, "devbox"); err != nil {
		t.Fatal(err)
	}

	// laptop has its own snapshot of the same project.
	laptop := gittest.New(t, "main")
	laptop.Commit("init", "tracked.txt", "v1\n")
	laptop.Write("laptop-work.txt", "local\n")
	laptopSnap, err := snapshot.New("laptop").Create(ctx, laptop.Path)
	if err != nil {
		t.Fatal(err)
	}

	// laptop fetches devbox's work.
	if _, _, err := For(cfg, "proj").FetchInto(ctx, laptop.Path, "devbox"); err != nil {
		t.Fatal(err)
	}

	// laptop's own snapshot must still be there.
	if sha := laptop.Git("rev-parse", laptopSnap.Ref); sha != laptopSnap.SHA {
		t.Errorf("laptop's own snapshot was disturbed: %s want %s", sha, laptopSnap.SHA)
	}
	// and devbox's is now present too.
	own, err := snapshot.List(ctx, laptop.Path, "devbox", "")
	if err != nil || len(own) != 1 {
		t.Errorf("devbox snapshot not fetched: %v (err %v)", own, err)
	}
}

func TestURLForLocalAndRemote(t *testing.T) {
	local := For(Config{Dir: "/srv/mirrors"}, "abc123")
	if got := local.url(); got != "/srv/mirrors/abc123.git" {
		t.Errorf("local url = %q", got)
	}

	remote := For(Config{Host: "devbox.ts.net", Dir: "/home/mads/mirrors"}, "abc123")
	if got := remote.url(); got != "devbox.ts.net:/home/mads/mirrors/abc123.git" {
		t.Errorf("remote url = %q", got)
	}
}

func TestSecondPushSendsUpdatesToExistingMirror(t *testing.T) {
	ctx := context.Background()
	cfg := LocalConfigForTest(t.TempDir())
	repo, first := dirtyRepoWithSnapshot(t, "devbox")

	m := For(cfg, "proj")
	if err := m.Push(ctx, repo.Path, "devbox"); err != nil {
		t.Fatal(err)
	}

	// More work, a new snapshot, another push to the now-existing mirror.
	repo.Write("more.txt", "additional\n")
	second, err := snapshot.New("devbox").Create(ctx, repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Push(ctx, repo.Path, "devbox"); err != nil {
		t.Fatalf("second push: %v", err)
	}

	bare := gittest.Repo{T: t, Path: filepath.Join(cfg.Dir, "proj.git")}
	if sha := bare.Git("rev-parse", second.Ref); sha != second.SHA {
		t.Errorf("mirror missing second snapshot: %s want %s", sha, second.SHA)
	}
	if sha := bare.Git("rev-parse", first.Ref); sha != first.SHA {
		t.Errorf("mirror lost first snapshot: %s want %s", sha, first.SHA)
	}
}

package collect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mads/poppy/internal/gittest"
	"github.com/mads/poppy/internal/model"
)

func TestRepoCollectsFullState(t *testing.T) {
	bare := gittest.NewBare(t, "main")
	repo := gittest.New(t, "main")
	repo.Commit("initial commit", "a.txt", "one\n")
	repo.Git("remote", "add", "origin", bare)
	repo.Git("push", "-q", "-u", "origin", "main")
	repo.Commit("unpushed work", "b.txt", "two\n")
	repo.Git("branch", "feature/login")
	repo.Write("dirty.txt", "wip\n")

	got := New("laptop").Repo(context.Background(), repo.Path, false, nil)

	if got.Error != "" {
		t.Fatalf("collect reported problems: %s", got.Error)
	}
	if got.Machine != "laptop" {
		t.Errorf("machine = %q", got.Machine)
	}
	if got.Head.Branch != "main" {
		t.Errorf("head branch = %q, want main", got.Head.Branch)
	}
	if got.Remotes["origin"] != bare {
		t.Errorf("origin = %q, want %q", got.Remotes["origin"], bare)
	}
	if got.Dirty.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", got.Dirty.Untracked)
	}
	if got.DirtySince == nil {
		t.Error("DirtySince = nil for a dirty tree")
	}
	if got.LastCommit == nil || got.LastCommit.Subject != "unpushed work" {
		t.Errorf("lastCommit = %+v, want subject 'unpushed work'", got.LastCommit)
	}
	// The commit time must parse; a trailing newline on the format's last
	// field silently zeroes it, which breaks every "last active" ordering.
	if got.LastCommit != nil && got.LastCommit.When.IsZero() {
		t.Error("lastCommit.When is zero; the timestamp failed to parse")
	}
	if got.LastCommit != nil && time.Since(got.LastCommit.When) > time.Hour {
		t.Errorf("lastCommit.When = %v, want a moment ago", got.LastCommit.When)
	}
	if got.RepoID == "" || got.PathID == "" {
		t.Errorf("ids not set: repoID=%q pathID=%q", got.RepoID, got.PathID)
	}

	byName := map[string]model.Branch{}
	for _, b := range got.Branches {
		byName[b.Name] = b
	}
	main, ok := byName["main"]
	if !ok {
		t.Fatalf("main branch missing from %+v", got.Branches)
	}
	if main.Ahead != 1 {
		t.Errorf("main ahead = %d, want 1", main.Ahead)
	}
	if main.Upstream != "origin/main" {
		t.Errorf("main upstream = %q, want origin/main", main.Upstream)
	}
	feature, ok := byName["feature/login"]
	if !ok {
		t.Fatalf("feature/login missing from %+v", got.Branches)
	}
	if feature.Upstream != "" {
		t.Errorf("feature upstream = %q, want empty", feature.Upstream)
	}
	if !feature.Unpushed() {
		t.Error("branch with no upstream should count as unpushed")
	}
}

// The age of stranded work is what makes it noticeable, so the moment a tree
// became dirty must survive rescans rather than resetting on each one.
func TestRepoCarriesDirtySinceForward(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("wip.txt", "work\n")

	earlier := time.Now().Add(-48 * time.Hour)
	c := New("laptop")
	c.Now = func() time.Time { return earlier }
	first := c.Repo(context.Background(), repo.Path, false, nil)
	if first.DirtySince == nil {
		t.Fatal("first scan recorded no DirtySince")
	}

	c.Now = time.Now
	second := c.Repo(context.Background(), repo.Path, false, &first)
	if second.DirtySince == nil {
		t.Fatal("second scan dropped DirtySince")
	}
	if !second.DirtySince.Equal(*first.DirtySince) {
		t.Errorf("DirtySince moved: %v -> %v", first.DirtySince, second.DirtySince)
	}
}

func TestRepoClearsDirtySinceWhenTreeIsCleaned(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("wip.txt", "work\n")

	c := New("laptop")
	dirty := c.Repo(context.Background(), repo.Path, false, nil)
	if dirty.DirtySince == nil {
		t.Fatal("expected DirtySince while dirty")
	}

	repo.Git("add", "-A")
	repo.Git("commit", "-q", "-m", "commit the work")

	clean := c.Repo(context.Background(), repo.Path, false, &dirty)
	if clean.DirtySince != nil {
		t.Errorf("DirtySince = %v, want nil after commit", clean.DirtySince)
	}
	if clean.Dirty.Any() {
		t.Errorf("dirty = %+v, want clean", clean.Dirty)
	}
}

// A tree that goes clean and later dirties again is new work, so the age must
// restart rather than report the original stale timestamp.
func TestRepoRestartsDirtySinceAfterCleanCycle(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("wip.txt", "work\n")

	old := time.Now().Add(-72 * time.Hour)
	c := New("laptop")
	c.Now = func() time.Time { return old }
	dirty := c.Repo(context.Background(), repo.Path, false, nil)

	repo.Git("add", "-A")
	repo.Git("commit", "-q", "-m", "done")
	c.Now = time.Now
	clean := c.Repo(context.Background(), repo.Path, false, &dirty)

	repo.Write("new-work.txt", "again\n")
	again := c.Repo(context.Background(), repo.Path, false, &clean)

	if again.DirtySince == nil {
		t.Fatal("no DirtySince after re-dirtying")
	}
	if again.DirtySince.Equal(old) {
		t.Error("DirtySince reused a stale timestamp from a previous dirty period")
	}
}

func TestRepoHandlesRepositoryWithoutRemote(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	got := New("laptop").Repo(context.Background(), repo.Path, false, nil)
	if got.Error != "" {
		t.Errorf("remoteless repo reported problems: %s", got.Error)
	}
	if len(got.Remotes) != 0 {
		t.Errorf("remotes = %v, want none", got.Remotes)
	}
	if got.RepoID == "" {
		t.Error("RepoID empty for remoteless repo")
	}
}

func TestRepoHandlesUnbornBranch(t *testing.T) {
	repo := gittest.New(t, "main")

	got := New("laptop").Repo(context.Background(), repo.Path, false, nil)
	if !got.Head.Unborn {
		t.Errorf("head = %+v, want unborn", got.Head)
	}
	if got.LastCommit != nil {
		t.Errorf("lastCommit = %+v, want nil", got.LastCommit)
	}
	if got.Error != "" {
		t.Errorf("unborn repo reported problems: %s", got.Error)
	}
}

func TestRepoCountsStashes(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("a.txt", "changed\n")
	repo.Git("stash", "push", "-q", "-m", "first")
	repo.Write("a.txt", "changed again\n")
	repo.Git("stash", "push", "-q", "-m", "second")

	got := New("laptop").Repo(context.Background(), repo.Path, false, nil)
	if got.Stashes != 2 {
		t.Errorf("stashes = %d, want 2", got.Stashes)
	}
}

func TestRepoListsLinkedWorktrees(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	wt := filepath.Join(t.TempDir(), "poppy-fix")
	repo.Git("worktree", "add", "-q", "-b", "fix", wt)

	got := New("laptop").Repo(context.Background(), repo.Path, false, nil)
	if len(got.Worktrees) != 1 {
		t.Fatalf("worktrees = %v, want 1", got.Worktrees)
	}
}

func TestRepoDegradesRatherThanFailingOnBrokenRepository(t *testing.T) {
	dir := t.TempDir()
	got := New("laptop").Repo(context.Background(), dir, false, nil)

	if got.Path == "" {
		t.Error("path not recorded for unreadable directory")
	}
	if !got.Broken {
		t.Error("Broken = false for a non-repository")
	}
	if got.Error == "" {
		t.Error("expected a recorded problem for a non-repository")
	}
}

// A worktree left behind after its parent repository is removed is invisible
// to every ordinary git command, so anything inside it is stranded. Poppy has
// to name that case rather than repeating git's fatal error for each command
// it would otherwise attempt.
func TestRepoIdentifiesOrphanedWorktree(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	wt := filepath.Join(t.TempDir(), "feature-work")
	repo.Git("worktree", "add", "-q", "-b", "feature", wt)

	admin := filepath.Join(repo.Path, ".git", "worktrees")
	if err := os.RemoveAll(admin); err != nil {
		t.Fatal(err)
	}

	got := New("laptop").Repo(context.Background(), wt, true, nil)

	if !got.Broken {
		t.Fatalf("Broken = false for an orphaned worktree: %+v", got)
	}
	if !strings.Contains(got.Error, "orphaned worktree") {
		t.Errorf("error = %q, want it to name the orphaned worktree", got.Error)
	}
	if strings.Count(got.Error, "fatal:") > 1 {
		t.Errorf("error repeats git failures instead of reporting the cause once: %q", got.Error)
	}
	if !got.AtRisk() {
		t.Error("orphaned worktree not reported as at risk")
	}
	if got.LocalOnly() {
		t.Error("unreadable repo miscounted as having no remote")
	}
}

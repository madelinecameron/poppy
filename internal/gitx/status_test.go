package gitx

import (
	"context"
	"testing"

	"github.com/madelinecameron/poppy/internal/gittest"
)

func TestParseStatusCountsEachKindOfChange(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "tracked.txt", "one\n")
	repo.Commit("second", "renamed-from.txt", "content\n")

	repo.Write("tracked.txt", "modified\n")
	repo.Write("untracked.txt", "new\n")
	repo.Write("staged.txt", "staged\n")
	repo.Git("add", "staged.txt")
	repo.Git("mv", "renamed-from.txt", "renamed-to.txt")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if st.Head.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Head.Branch)
	}
	if st.Dirty.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", st.Dirty.Untracked)
	}
	if st.Dirty.Unstaged != 1 {
		t.Errorf("unstaged = %d, want 1 (tracked.txt)", st.Dirty.Unstaged)
	}
	// staged.txt added plus the rename, both staged.
	if st.Dirty.Staged != 2 {
		t.Errorf("staged = %d, want 2", st.Dirty.Staged)
	}
	if !st.Dirty.Any() {
		t.Error("Any() = false, want true")
	}
}

// A rename record carries its original path in a following NUL field. If that
// field is not consumed it is misread as a record of its own, inflating counts
// and corrupting the file list.
func TestParseStatusConsumesRenameOriginalPath(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "original.txt", "content\n")
	repo.Git("mv", "original.txt", "renamed.txt")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if got := st.Dirty.Total(); got != 1 {
		t.Errorf("total = %d, want 1; rename original path likely parsed as its own record", got)
	}
	if len(st.Dirty.Files) != 1 || st.Dirty.Files[0] != "renamed.txt" {
		t.Errorf("files = %v, want [renamed.txt]", st.Dirty.Files)
	}
}

func TestParseStatusHandlesPathsWithSpaces(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("a file with spaces.txt", "content\n")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Dirty.Files) != 1 || st.Dirty.Files[0] != "a file with spaces.txt" {
		t.Errorf("files = %v, want [a file with spaces.txt]", st.Dirty.Files)
	}
}

func TestParseStatusCleanRepo(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Dirty.Any() {
		t.Errorf("Any() = true for clean repo: %+v", st.Dirty)
	}
}

func TestParseStatusUnbornBranch(t *testing.T) {
	repo := gittest.New(t, "main")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Head.Unborn {
		t.Errorf("Unborn = false, want true for a repo with no commits")
	}
	if st.Head.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Head.Branch)
	}
}

func TestParseStatusDetachedHead(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Commit("second", "b.txt", "two\n")
	repo.Git("checkout", "-q", "HEAD~1")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Head.Detached {
		t.Error("Detached = false, want true")
	}
}

func TestParseStatusAheadBehind(t *testing.T) {
	bare := gittest.NewBare(t, "main")
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Git("remote", "add", "origin", bare)
	repo.Git("push", "-q", "-u", "origin", "main")
	repo.Commit("local work", "b.txt", "two\n")

	st, err := New(repo.Path).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Ahead != 1 {
		t.Errorf("ahead = %d, want 1", st.Ahead)
	}
	if st.Behind != 0 {
		t.Errorf("behind = %d, want 0", st.Behind)
	}
	if st.Upstream != "origin/main" {
		t.Errorf("upstream = %q, want origin/main", st.Upstream)
	}
}

func TestMaxDirtyFilesTruncates(t *testing.T) {
	fields := make([]string, 0, MaxDirtyFiles+50)
	for i := range MaxDirtyFiles + 50 {
		fields = append(fields, "? file"+string(rune('a'+i%26))+string(rune('0'+i/26%10)))
	}
	st := parseStatus(fields)
	if len(st.Dirty.Files) != MaxDirtyFiles {
		t.Errorf("files = %d, want %d", len(st.Dirty.Files), MaxDirtyFiles)
	}
	if !st.Dirty.Truncated {
		t.Error("Truncated = false, want true")
	}
	if st.Dirty.Untracked != MaxDirtyFiles+50 {
		t.Errorf("untracked count = %d, want full count %d", st.Dirty.Untracked, MaxDirtyFiles+50)
	}
}

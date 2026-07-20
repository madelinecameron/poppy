package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mads/poppy/internal/gittest"
)

// repoAt creates a repository and returns its physical path, which is what a
// scan reports after resolving symlinks.
func repoAt(t *testing.T, root, rel string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &gittest.Repo{T: t, Path: path}
	r.Git("init", "-q", "-b", "main", ".")
	return physical(t, path)
}

func physical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func paths(found []Found) []string {
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.Path
	}
	slices.Sort(out)
	return out
}

func TestRunFindsRepositoriesBeneathRoots(t *testing.T) {
	root := t.TempDir()
	a := repoAt(t, root, "personal/poppy")
	b := repoAt(t, root, "work/api")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := Run(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	got := paths(found)
	want := []string{a, b}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("found %v, want %v", got, want)
	}
}

// Vendored dependencies contain thousands of repositories that are not the
// user's work; descending into them would swamp the fleet view.
func TestRunSkipsHeavyDirectories(t *testing.T) {
	root := t.TempDir()
	wanted := repoAt(t, root, "project")
	repoAt(t, root, "project-b/node_modules/dep")
	repoAt(t, root, "project-c/.venv/lib/pkg")

	found, err := Run(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.Path != wanted {
			t.Errorf("descended into a skipped directory: %s", f.Path)
		}
	}
}

// A repository inside a repository is a submodule or a vendored copy; poppy
// tracks the outer working tree only.
func TestRunDoesNotDescendIntoRepositories(t *testing.T) {
	root := t.TempDir()
	outer := repoAt(t, root, "outer")
	repoAt(t, root, "outer/nested")

	found, err := Run(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(found); !slices.Equal(got, []string{outer}) {
		t.Errorf("found %v, want only %v", got, outer)
	}
}

func TestRunRespectsMaxDepth(t *testing.T) {
	root := t.TempDir()
	repoAt(t, root, "a/b/c/d/deep")

	found, err := Run(context.Background(), Options{Roots: []string{root}, MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("found %v beyond max depth", paths(found))
	}
}

func TestRunFindsLinkedWorktreesWhenEnabled(t *testing.T) {
	root := t.TempDir()
	main := repoAt(t, root, "poppy")
	r := &gittest.Repo{T: t, Path: main}
	r.Git("config", "user.email", "t@example.com")
	r.Git("config", "user.name", "t")
	r.Commit("init", "a.txt", "one\n")
	wt := filepath.Join(root, "poppy-fix")
	r.Git("worktree", "add", "-q", "-b", "fix", wt)
	wt = physical(t, wt)

	with, err := Run(context.Background(), Options{Roots: []string{root}, IncludeWorktrees: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(with) != 2 {
		t.Errorf("with worktrees: found %v, want 2 entries", paths(with))
	}
	var sawWorktree bool
	for _, f := range with {
		if f.Path == wt && f.IsWorktree {
			sawWorktree = true
		}
	}
	if !sawWorktree {
		t.Errorf("linked worktree not flagged: %+v", with)
	}

	without, err := Run(context.Background(), Options{Roots: []string{root}, IncludeWorktrees: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(without); !slices.Equal(got, []string{main}) {
		t.Errorf("without worktrees: found %v, want only %v", got, main)
	}
}

func TestRunIgnoresMissingRoots(t *testing.T) {
	found, err := Run(context.Background(), Options{Roots: []string{"/nonexistent/path/xyz"}})
	if err != nil {
		t.Fatalf("missing root should not fail: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %v", paths(found))
	}
}

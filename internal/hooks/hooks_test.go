package hooks_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/hooks"
)

const handWritten = "#!/bin/sh\n# mine, do not touch\nmake lint\n"

func TestInstallIntoRepositoryWithoutHook(t *testing.T) {
	repo := newRepo(t)

	if st := hooks.Verify(context.Background(), repo.Path); st.State != hooks.StateMissing {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateMissing)
	}

	st, err := hooks.Install(context.Background(), repo.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != hooks.StateInstalled {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateInstalled)
	}

	info, err := os.Stat(st.Hook)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("hook mode = %v, want executable", info.Mode().Perm())
	}
	if again := hooks.Verify(context.Background(), repo.Path); again.State != hooks.StateInstalled {
		t.Errorf("verify after install = %q", again.State)
	}
}

// TestInstallRefusesToClobber is the property that decides whether this is
// safe to run across 138 repositories at once.
func TestInstallRefusesToClobber(t *testing.T) {
	repo := newRepo(t)
	hook := writeHook(t, repo, handWritten)

	st, err := hooks.Install(context.Background(), repo.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != hooks.StateForeign {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateForeign)
	}

	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != handWritten {
		t.Fatalf("an existing hook was modified without --force:\n%s", body)
	}
	if _, err := os.Stat(hook + ".poppy-backup"); err == nil {
		t.Error("a backup was written even though nothing was replaced")
	}
}

func TestForceInstallKeepsTheDisplacedHook(t *testing.T) {
	repo := newRepo(t)
	hook := writeHook(t, repo, handWritten)

	st, err := hooks.Install(context.Background(), repo.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != hooks.StateInstalled {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateInstalled)
	}

	backup, err := os.ReadFile(hook + ".poppy-backup")
	if err != nil {
		t.Fatalf("the displaced hook was not kept: %v", err)
	}
	if string(backup) != handWritten {
		t.Errorf("backup = %q, want the original hook", backup)
	}
}

func TestUninstallRestoresTheDisplacedHook(t *testing.T) {
	repo := newRepo(t)
	hook := writeHook(t, repo, handWritten)
	if _, err := hooks.Install(context.Background(), repo.Path, true); err != nil {
		t.Fatal(err)
	}

	st, err := hooks.Uninstall(context.Background(), repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != hooks.StateForeign {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateForeign)
	}
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != handWritten {
		t.Errorf("restored hook = %q, want the original", body)
	}
}

func TestUninstallLeavesAForeignHookAlone(t *testing.T) {
	repo := newRepo(t)
	hook := writeHook(t, repo, handWritten)

	if _, err := hooks.Uninstall(context.Background(), repo.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("a hook poppy did not write was removed: %v", err)
	}
}

func TestOutdatedHookIsRecognised(t *testing.T) {
	repo := newRepo(t)
	writeHook(t, repo, "#!/bin/sh\n# poppy-managed pre-commit hook (v0)\nexit 0\n")

	if st := hooks.Verify(context.Background(), repo.Path); st.State != hooks.StateOutdated {
		t.Fatalf("state = %q, want %q", st.State, hooks.StateOutdated)
	}
	// Refreshing poppy's own hook is not clobbering, so it needs no flag.
	st, err := hooks.Install(context.Background(), repo.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != hooks.StateInstalled {
		t.Errorf("state = %q, want %q", st.State, hooks.StateInstalled)
	}
}

func TestHookPathHonoursCoreHooksPath(t *testing.T) {
	repo := newRepo(t)
	custom := filepath.Join(repo.Path, "githooks")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("config", "core.hooksPath", custom)

	st, err := hooks.Install(context.Background(), repo.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(custom, "pre-commit"); st.Hook != want {
		t.Errorf("hook = %q, want %q", st.Hook, want)
	}
}

func newRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	repo := gittest.New(t, "main")
	repo.Commit("init", "README.md", "hello\n")
	return repo
}

func writeHook(t *testing.T, repo *gittest.Repo, body string) string {
	t.Helper()
	dir := filepath.Join(repo.Path, ".git", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pre-commit")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
